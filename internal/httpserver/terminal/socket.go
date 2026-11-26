package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"piped/internal/execution"
	"piped/internal/httpserver/response"
	"piped/internal/lifecycle"
	"piped/internal/store"
)

// Terminal sockets use browser sessions only. Explicit origin validation is
// required because a browser WebSocket cannot send the HTTP mutation header.
func (s *Handler) Serve(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	if r.Header.Get("Origin") != s.origin {
		return response.Fail(403, "cross-origin terminal rejected")
	}
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		return response.Fail(400, "invalid output cursor")
	}
	channel := &terminalChannel{kind: r.URL.Query().Get("channel"), acknowledged: make(chan struct{}, 1)}
	if channel.kind != "" && channel.kind != "control" && channel.kind != "output" {
		return response.Fail(400, "invalid terminal channel")
	}
	runID, hostID := r.PathValue("run"), r.PathValue("host")
	run, err := s.store.Run(r.Context(), actor, runID)
	if err != nil {
		return response.ExecutionFailure(err)
	}
	found := false
	for _, host := range run.Hosts {
		if host.HostID == hostID {
			found = true
			break
		}
	}
	if !found {
		return response.Fail(404, "execution host not found")
	}
	if !s.allow("terminal:"+actor.SessionHash, time.Now()) {
		return response.Fail(429, "too many terminal connections")
	}
	if !s.acquireTerminal(actor.SessionHash) {
		return response.Fail(429, "too many open terminals")
	}
	defer s.releaseTerminal(actor.SessionHash)
	changes, unsubscribe := s.store.WatchRun(runID)
	if channel.kind == "control" {
		unsubscribe()
		changes, unsubscribe = s.store.WatchRunState(runID)
	}
	defer unsubscribe()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{s.origin}})
	if err != nil {
		return nil
	} // Accept already wrote the handshake error.
	defer conn.CloseNow()
	conn.SetReadLimit(32 * 1024)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var inputs atomic.Int64
	inputDone := make(chan struct{}, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer cancel()
		s.readTerminalMessages(ctx, conn, actor, runID, hostID, &inputs, inputDone, channel)
	}()
	// Join the reader on every channel before releasing the connection quota.
	defer func() {
		cancel()
		_ = conn.CloseNow()
		<-readerDone
	}()
	err = s.streamTerminal(ctx, conn, actor, runID, hostID, after, changes, inputDone, &inputs, channel)
	if errors.Is(err, store.ErrUnauthorized) {
		_ = conn.Close(4001, "sign in required")
	}
	return nil
}

// Read status before output: a completed snapshot guarantees all final chunks
// were committed before the subsequent cursor reads. Wakeups cover later changes.
func (s *Handler) pushTerminal(ctx context.Context, conn *websocket.Conn, actor store.Actor, runID, hostID string, after *int64, channel *terminalChannel) (bool, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, lifecycle.StorageTimeout)
	defer cancel()
	run, err := s.store.Run(ctx, actor, runID)
	if err != nil {
		return false, false, err
	}
	more := false
	pages := 0
	switch channel.kind {
	case "":
		pages = 4
	case "output":
		pages = 1
	}
	for page := 0; page < pages; page++ {
		chunks, err := s.store.RunOutput(ctx, actor, runID, hostID, *after)
		if err != nil {
			return false, false, err
		}
		more = len(chunks) == 16
		if len(chunks) > 0 {
			*after = chunks[len(chunks)-1].ID
			if channel.kind == "output" {
				channel.pending.Store(*after)
				// Even a partial final page must be acknowledged before completion.
				more = true
			}
			if err := writeTerminal(ctx, conn, map[string]any{"type": "output", "chunks": chunks}); err != nil {
				return false, false, err
			}
		}
		if !more {
			break
		}
	}
	if channel.kind != "output" {
		keys, err := s.executions.Pending(ctx, actor, runID)
		if err != nil {
			return false, false, err
		}
		if err := s.store.Authorize(ctx, actor, store.ScopeRunsRead); err != nil {
			return false, false, err
		}
		if err := writeTerminal(ctx, conn, map[string]any{"type": "state", "run": run, "host_keys": keys}); err != nil {
			return false, false, err
		}
	}
	return more, run.FinishedAt != 0 && channel.kind != "control", nil
}

func writeTerminal(ctx context.Context, conn *websocket.Conn, message any) error {
	writeCtx, cancel := context.WithTimeout(ctx, lifecycle.TerminalWriteTimeout)
	defer cancel()
	return wsjson.Write(writeCtx, conn, message)
}

func (s *Handler) readTerminalMessages(ctx context.Context, conn *websocket.Conn, actor store.Actor, runID, hostID string, inputs *atomic.Int64, done chan<- struct{}, channel *terminalChannel) {
	var lastID int64
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var message struct {
			Type   string `json:"type"`
			ID     int64  `json:"id"`
			Data   string `json:"data,omitempty"`
			Before int64  `json:"before,omitempty"`
			Offset int    `json:"offset,omitempty"`
			After  int64  `json:"after,omitempty"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&message)
		var extra any
		if kind != websocket.MessageText || decodeErr != nil || decoder.Decode(&extra) != io.EOF || message.ID <= lastID || message.ID > 9007199254740991 {
			_ = conn.Close(websocket.StatusPolicyViolation, "invalid terminal input")
			return
		}
		if message.Type == "output_ack" {
			if channel.kind != "output" || message.Data != "" || message.Before != 0 || message.Offset != 0 || message.After < 1 || !channel.pending.CompareAndSwap(message.After, 0) {
				_ = conn.Close(websocket.StatusPolicyViolation, "invalid output acknowledgement")
				return
			}
			if err := s.store.Authorize(ctx, actor, store.ScopeRunsRead); err != nil {
				_ = conn.Close(4001, "sign in required")
				return
			}
			lastID = message.ID
			select {
			case channel.acknowledged <- struct{}{}:
			default:
			}
			continue
		}
		if message.Type == "history" {
			if channel.kind == "control" || message.Data != "" || message.Before < 1 || message.Offset < 0 || message.Offset > 4096 || message.After != 0 {
				_ = conn.Close(websocket.StatusPolicyViolation, "invalid history request")
				return
			}
			lastID = message.ID
			if err := s.sendTerminalHistory(ctx, conn, actor, runID, hostID, message.ID, message.Before, message.Offset); err != nil {
				return
			}
			continue
		}
		if channel.kind == "output" || message.Type != "input" || len(message.Data) == 0 || len(message.Data) > 4096 || message.Before != 0 || message.Offset != 0 || message.After != 0 {
			_ = conn.Close(websocket.StatusPolicyViolation, "invalid terminal input")
			return
		}

		if !s.allow("terminal-input:"+actor.SessionHash, time.Now()) {
			_ = conn.Close(websocket.StatusPolicyViolation, "too much terminal input")
			return
		}
		lastID = message.ID
		inputs.Add(1)
		err = s.executions.Input(ctx, actor, execution.InputRequest{RunID: runID, HostID: hostID, Data: message.Data})
		message.Data = ""
		result := map[string]any{"type": "input_result", "id": message.ID}
		if err != nil {
			if errors.Is(err, store.ErrUnauthorized) {
				inputs.Add(-1)
				_ = conn.Close(4001, "sign in required")
				return
			}
			result["error"] = "Execution no longer accepts input."
		}
		writeErr := writeTerminal(ctx, conn, result)
		inputs.Add(-1)
		select {
		case done <- struct{}{}:
		default:
		}
		if writeErr != nil {
			return
		}
	}
}

func (s *Handler) sendTerminalHistory(ctx context.Context, conn *websocket.Conn, actor store.Actor, runID, hostID string, id, before int64, offset int) error {
	ctx, cancel := context.WithTimeout(ctx, lifecycle.StorageTimeout)
	defer cancel()
	result := map[string]any{"type": "history", "id": id}
	if !s.allow("terminal-history:"+actor.SessionHash, time.Now()) {
		result["error"] = "Too many history requests. Try again shortly."
		return writeTerminal(ctx, conn, result)
	}
	chunks, more, err := s.store.RunOutputBefore(ctx, actor, runID, hostID, before, offset)
	if errors.Is(err, store.ErrUnauthorized) {
		_ = conn.Close(4001, "sign in required")
		return err
	}
	if err != nil {
		result["error"] = "Could not load saved output."
	} else {
		if err := s.store.Authorize(ctx, actor, store.ScopeRunsRead); err != nil {
			_ = conn.Close(4001, "sign in required")
			return err
		}
		result["chunks"], result["has_more"] = chunks, more
	}
	return writeTerminal(ctx, conn, result)
}

func (s *Handler) acquireTerminal(session string) bool {
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	if s.terminalTotal >= 64 || s.terminalCounts[session] >= 4 {
		return false
	}
	if s.terminalCounts == nil {
		s.terminalCounts = make(map[string]int)
	}
	s.terminalCounts[session]++
	s.terminalTotal++
	return true
}

func (s *Handler) releaseTerminal(session string) {
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	s.terminalCounts[session]--
	s.terminalTotal--
	if s.terminalCounts[session] == 0 {
		delete(s.terminalCounts, session)
	}
}
