package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"piped/internal/auth"
	"piped/internal/store"
)

type terminalFixture struct {
	h      *harness
	actor  store.Actor
	cookie *http.Cookie
	run    store.Run
	host   store.Host
	server *httptest.Server
}

func newTerminalFixture(t *testing.T) *terminalFixture {
	t.Helper()
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	hostResponse := h.request("POST", "/api/hosts", hostBody(), cookie)
	expect(t, hostResponse, 201)
	host := responseHost(t, hostResponse)
	jobResponse := h.request("POST", "/api/jobs", jobBody(host.ID), cookie)
	expect(t, jobResponse, 201)
	job := responseJob(t, jobResponse)
	actor, err := h.auth.Resolve(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	runAdmission, err := h.store.AdmitRun(context.Background(), actor, store.RunAdmission{ID: "socket-fixture", RequestID: "socket-fixture", JobID: job.ID, Version: job.Version})
	run := runAdmission.PublicRun()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.handler)
	t.Cleanup(server.Close)
	return &terminalFixture{h, actor, cookie, run, host, server}
}

func (f *terminalFixture) path(after int64) string {
	return f.server.URL + "/api/runs/" + f.run.ID + "/hosts/" + f.host.ID + "/terminal?after=" + strconv.FormatInt(after, 10)
}
func (f *terminalFixture) dial(t *testing.T, after int64) *websocket.Conn {
	t.Helper()
	headers := http.Header{"Origin": []string{f.h.origin}, "Cookie": []string{f.cookie.String()}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, f.path(after), &websocket.DialOptions{Host: strings.TrimPrefix(f.h.origin, "http://"), HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadLimit(1024 * 1024)
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}
func readTerminalMessage(t *testing.T, conn *websocket.Conn) map[string]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var message map[string]json.RawMessage
	if err := wsjson.Read(ctx, conn, &message); err != nil {
		t.Fatal(err)
	}
	return message
}
func terminalMessageType(message map[string]json.RawMessage) string {
	var kind string
	_ = json.Unmarshal(message["type"], &kind)
	return kind
}
func waitTerminalState(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	for range 20 {
		if terminalMessageType(readTerminalMessage(t, conn)) == "state" {
			return
		}
	}
	t.Fatal("no terminal state")
}

func TestUpgradedTerminalOutlivesHandshakeDeadline(t *testing.T) {
	f := newTerminalFixture(t)
	f.server.Close()
	contexts := make(chan context.Context, 1)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		contexts <- ctx
		f.h.handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(f.server.Close)
	connection := f.dial(t, 0)
	waitTerminalState(t, connection)
	request := <-contexts
	select {
	case <-request.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("handshake request deadline did not expire")
	}
	if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("after handshake deadline")); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		message := readTerminalMessage(t, connection)
		if terminalMessageType(message) == "output" {
			var chunks []store.OutputChunk
			if err := json.Unmarshal(message["chunks"], &chunks); err != nil {
				t.Fatal(err)
			}
			if len(chunks) == 1 && string(chunks[0].Data) == "after handshake deadline" {
				return
			}
		}
	}
	t.Fatal("API deadline stopped an already upgraded terminal")
}

func TestTerminalSocketOriginSessionAndHostChecks(t *testing.T) {
	f := newTerminalFixture(t)
	cases := []struct {
		name, origin, cookie, path string
		status                     int
	}{
		{"signed out", f.h.origin, "", f.path(0), 401},
		{"missing origin", "", f.cookie.String(), f.path(0), 403},
		{"wrong origin", "https://untrusted.example", f.cookie.String(), f.path(0), 403},
		{"wrong scheme", strings.Replace(f.h.origin, "http:", "https:", 1), f.cookie.String(), f.path(0), 403},
		{"cursor", f.h.origin, f.cookie.String(), f.path(-1), 400},
		{"channel", f.h.origin, f.cookie.String(), f.path(0) + "&channel=unknown", 400},
		{"missing host", f.h.origin, f.cookie.String(), strings.Replace(f.path(0), f.host.ID, strings.Repeat("a", 32), 1), 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, response, err := websocket.Dial(ctx, tc.path, &websocket.DialOptions{Host: strings.TrimPrefix(f.h.origin, "http://"), HTTPHeader: http.Header{"Origin": []string{tc.origin}, "Cookie": []string{tc.cookie}}})
			if conn != nil {
				conn.CloseNow()
			}
			if err == nil || response == nil || response.StatusCode != tc.status {
				t.Fatalf("handshake status: %v %v", response, err)
			}
		})
	}
}

func TestTerminalSocketResumesPersistsAndFlushesFinalOutput(t *testing.T) {
	f := newTerminalFixture(t)
	var expected strings.Builder
	for index := 0; index < 100; index++ {
		value := "packet-" + strconv.Itoa(index) + "\n"
		if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte(value)); err != nil {
			t.Fatal(err)
		}
		if index >= 16 {
			expected.WriteString(value)
		}
	}
	chunks, err := f.h.store.RunOutput(context.Background(), f.actor, f.run.ID, f.host.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := f.dial(t, chunks[15].ID)
	var output strings.Builder
	var lastID = chunks[15].ID
	complete := false
	// Append final output concurrently with the stream's initial replay.
	if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("final packet\n")); err != nil {
		t.Fatal(err)
	}
	expected.WriteString("final packet\n")
	code := 0
	if err := f.h.store.SetRunHost(context.Background(), f.run.ID, f.host.ID, "succeeded", "", &code); err != nil {
		t.Fatal(err)
	}
	// Replace the source rows while this connection is replaying a live cursor.
	if err := f.h.store.CompressRunOutput(context.Background(), f.run.ID, f.host.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.FinishRun(context.Background(), f.run.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	finalState := false
	for range 30 {
		message := readTerminalMessage(t, conn)
		switch terminalMessageType(message) {
		case "output":
			var chunks []store.OutputChunk
			if err := json.Unmarshal(message["chunks"], &chunks); err != nil {
				t.Fatal(err)
			}
			for _, chunk := range chunks {
				if chunk.ID <= lastID {
					t.Fatal("cursor replayed or reordered output")
				}
				lastID = chunk.ID
				output.Write(chunk.Data)
			}
		case "state":
			var run store.Run
			if err := json.Unmarshal(message["run"], &run); err != nil {
				t.Fatal(err)
			}
			finalState = run.FinishedAt != 0 && run.Hosts[0].ExitCode != nil && *run.Hosts[0].ExitCode == 0
		case "complete":
			complete = true
		}
		if complete {
			break
		}
	}
	if !complete || !finalState || output.String() != expected.String() {
		t.Fatalf("stream missed final state/output: complete=%v final=%v bytes=%d want=%d", complete, finalState, output.Len(), expected.Len())
	}
	persisted, _, err := f.h.store.RunOutputTail(context.Background(), f.actor, f.run.ID, f.host.ID)
	if err != nil || len(persisted) != 101 {
		t.Fatal("stream changed persisted history")
	}
}

func TestTerminalSocketPushesChangesAndRevokesSession(t *testing.T) {
	f := newTerminalFixture(t)
	conn := f.dial(t, 0)
	waitTerminalState(t, conn)
	if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("new output")); err != nil {
		t.Fatal(err)
	}
	message := readTerminalMessage(t, conn)
	if terminalMessageType(message) != "output" {
		t.Fatal("output not pushed on commit")
	}
	waitTerminalState(t, conn)
	if err := f.h.store.DeleteSession(context.Background(), auth.TokenHash(f.cookie.Value)); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("must not reach revoked session")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	if websocket.CloseStatus(err) != 4001 {
		t.Fatalf("revoked stream stayed open: %v", err)
	}
}

func TestTerminalSocketBoundsInputAndAcknowledgesErrors(t *testing.T) {
	f := newTerminalFixture(t)
	conn := f.dial(t, 0)
	waitTerminalState(t, conn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "input", "id": 1, "data": "private test input\n"}); err != nil {
		t.Fatal(err)
	}
	acknowledged := false
	for range 4 {
		message := readTerminalMessage(t, conn)
		if terminalMessageType(message) == "input_result" {
			acknowledged = true
			if string(message["id"]) != "1" || len(message["error"]) == 0 || strings.Contains(string(message["error"]), "private") {
				t.Fatal("input acknowledgement exposed data or lost error")
			}
			break
		}
	}
	if !acknowledged {
		t.Fatal("input was not acknowledged")
	}
	// Reused IDs must not deliver an input twice.
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "input", "id": 1, "data": "private test input\n"}); err != nil {
		t.Fatal(err)
	}
	for {
		_, _, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatal(err)
			}
			break
		}
	}
	oversized := f.dial(t, 0)
	waitTerminalState(t, oversized)
	if err := wsjson.Write(ctx, oversized, map[string]any{"type": "input", "id": 1, "data": strings.Repeat("x", 4097)}); err != nil {
		t.Fatal(err)
	}
	for {
		_, _, err := oversized.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatal(err)
			}
			break
		}
	}
}

func TestTerminalSocketLimitsConcurrentConnections(t *testing.T) {
	for _, channel := range []string{"", "control", "output"} {
		t.Run("channel="+channel, func(t *testing.T) {
			f := newTerminalFixture(t)
			if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("output")); err != nil {
				t.Fatal(err)
			}
			var connections []*websocket.Conn
			for range 4 {
				var conn *websocket.Conn
				if channel == "" {
					conn = f.dial(t, 0)
					waitTerminalState(t, conn)
				} else {
					conn = f.dialChannel(t, channel)
					readTerminalMessage(t, conn)
				}
				connections = append(connections, conn)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, response, err := websocket.Dial(ctx, f.path(0)+"&channel="+channel, &websocket.DialOptions{Host: strings.TrimPrefix(f.h.origin, "http://"), HTTPHeader: http.Header{"Origin": []string{f.h.origin}, "Cookie": []string{f.cookie.String()}}})
			if err == nil || response == nil || response.StatusCode != 429 {
				t.Fatal("connection bound ignored")
			}
			for _, conn := range connections {
				conn.CloseNow()
			}
		})
	}
}

func TestTerminalHistoryPagesRecoverEveryByteAfterCompletion(t *testing.T) {
	f := newTerminalFixture(t)
	var original bytes.Buffer
	for index := 0; index < 220; index++ {
		data := bytes.Repeat([]byte{byte('A' + index%26)}, 4001)
		original.Write(data)
		if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, data); err != nil {
			t.Fatal(err)
		}
	}
	tail, more, err := f.h.store.RunOutputTail(context.Background(), f.actor, f.run.ID, f.host.ID)
	if err != nil || !more || len(tail) == 0 || tail[0].Offset == 0 {
		t.Fatal("fixture did not split the first chunk")
	}
	var assembled []byte
	for _, chunk := range tail {
		assembled = append(assembled, chunk.Data...)
	}
	cursor := tail[0]
	code := 0
	if err := f.h.store.SetRunHost(context.Background(), f.run.ID, f.host.ID, "succeeded", "", &code); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.FinishRun(context.Background(), f.run.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	conn := f.dial(t, tail[len(tail)-1].ID)
	for range 3 {
		if terminalMessageType(readTerminalMessage(t, conn)) == "complete" {
			break
		}
	}
	for id := int64(1); more; id++ {
		if id > 10 {
			t.Fatal("history cursor failed to advance")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := wsjson.Write(ctx, conn, map[string]any{"type": "history", "id": id, "before": cursor.ID, "offset": cursor.Offset})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]json.RawMessage
		for range 5 {
			response = readTerminalMessage(t, conn)
			if terminalMessageType(response) == "history" {
				break
			}
		}
		if len(response["error"]) != 0 || string(response["id"]) != strconv.FormatInt(id, 10) {
			t.Fatalf("history response failed: %s", response["error"])
		}
		var chunks []store.OutputChunk
		if err := json.Unmarshal(response["chunks"], &chunks); err != nil || len(chunks) == 0 {
			t.Fatal("history page empty before start")
		}
		if err := json.Unmarshal(response["has_more"], &more); err != nil {
			t.Fatal(err)
		}
		var page bytes.Buffer
		for _, chunk := range chunks {
			page.Write(chunk.Data)
		}
		if page.Len() > 256*1024 {
			t.Fatal("history page exceeded its bound")
		}
		assembled = append(page.Bytes(), assembled...)
		if chunks[0].ID > cursor.ID || chunks[0].ID == cursor.ID && chunks[0].Offset >= cursor.Offset {
			t.Fatal("history cursor repeated")
		}
		cursor = chunks[0]
	}
	if !bytes.Equal(assembled, original.Bytes()) {
		t.Fatalf("history lost or repeated bytes: got %d want %d", len(assembled), original.Len())
	}
	if cursor.Offset != 0 {
		t.Fatal("did not reach beginning of first chunk")
	}
	// An exhausted cursor still replies cleanly on the same completed socket.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "history", "id": 20, "before": cursor.ID, "offset": 0}); err != nil {
		t.Fatal(err)
	}
	response := readTerminalMessage(t, conn)
	if terminalMessageType(response) != "history" || string(response["has_more"]) != "false" || string(response["chunks"]) != "[]" {
		t.Fatal("exhausted history did not terminate")
	}
	// History access must also honor revocation on a completed run.
	if err := f.h.store.DeleteSession(context.Background(), auth.TokenHash(f.cookie.Value)); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "history", "id": 21, "before": cursor.ID, "offset": 0}); err != nil {
		t.Fatal(err)
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != 4001 {
		t.Fatalf("completed history ignored revoked session: %v", err)
	}
}

func TestTerminalHistoryRejectsInvalidOffsetsAndCrossHostCursors(t *testing.T) {
	f := newTerminalFixture(t)
	if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("bounded")); err != nil {
		t.Fatal(err)
	}
	chunks, err := f.h.store.RunOutput(context.Background(), f.actor, f.run.ID, f.host.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.h.store.RunOutputBefore(context.Background(), f.actor, f.run.ID, f.host.ID, chunks[0].ID, 8)
	if !errors.Is(err, store.ErrOutputCursor) {
		t.Fatal("offset beyond chunk accepted")
	}
	for _, offset := range []int{-1, 4097} {
		if _, _, err := f.h.store.RunOutputBefore(context.Background(), f.actor, f.run.ID, f.host.ID, chunks[0].ID, offset); !errors.Is(err, store.ErrOutputCursor) {
			t.Fatal("invalid offset accepted")
		}
	}
	if _, _, err := f.h.store.RunOutputBefore(context.Background(), f.actor, f.run.ID, "missing-host", chunks[0].ID, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("history host binding ignored")
	}
	conn := f.dial(t, chunks[0].ID)
	waitTerminalState(t, conn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "history", "id": 1, "before": chunks[0].ID, "offset": 8}); err != nil {
		t.Fatal(err)
	}
	response := readTerminalMessage(t, conn)
	if terminalMessageType(response) != "history" || len(response["error"]) == 0 {
		t.Fatal("history cursor failure not acknowledged")
	}
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "history", "id": 2, "before": chunks[0].ID, "offset": -1}); err != nil {
		t.Fatal(err)
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("malformed history frame accepted: %v", err)
	}
}
