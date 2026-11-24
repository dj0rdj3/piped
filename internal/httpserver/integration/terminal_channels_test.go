package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"piped/internal/auth"
	"piped/internal/store"
)

func (f *terminalFixture) dialChannel(t *testing.T, kind string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, f.path(0)+"&channel="+kind, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{f.h.origin}, "Cookie": []string{f.cookie.String()}}})
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadLimit(1024 * 1024)
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func outputCursor(t *testing.T, message map[string]json.RawMessage) int64 {
	t.Helper()
	if terminalMessageType(message) != "output" {
		t.Fatal("missing output page")
	}
	var chunks []store.OutputChunk
	if err := json.Unmarshal(message["chunks"], &chunks); err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 || len(chunks) > 16 {
		t.Fatal("output page exceeded its bound")
	}
	return chunks[len(chunks)-1].ID
}

func TestControlStatusAndInputDoNotWaitForOutputReplay(t *testing.T) {
	f := newTerminalFixture(t)
	for range 100 {
		if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("packet\n")); err != nil {
			t.Fatal(err)
		}
	}
	output := f.dialChannel(t, "output")
	cursor := outputCursor(t, readTerminalMessage(t, output))
	control := f.dialChannel(t, "control")
	waitTerminalState(t, control)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// The output connection is deliberately paused without acknowledging its page.
	// Even an input error must be acknowledged on the independent control stream.
	if err := wsjson.Write(ctx, control, map[string]any{"type": "input", "id": 1, "data": "\x03"}); err != nil {
		t.Fatal(err)
	}
	if kind := terminalMessageType(readTerminalMessage(t, control)); kind != "input_result" {
		t.Fatal("input acknowledgement waited behind output")
	}
	code := 130
	if err := f.h.store.SetRunHost(ctx, f.run.ID, f.host.ID, "interrupted", "", &code); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.FinishRun(ctx, f.run.ID, "interrupted"); err != nil {
		t.Fatal(err)
	}
	finished := false
	for range 5 {
		message := readTerminalMessage(t, control)
		if terminalMessageType(message) != "state" {
			t.Fatal("control connection carried log traffic")
		}
		var run store.Run
		if err := json.Unmarshal(message["run"], &run); err != nil {
			t.Fatal(err)
		}
		if run.FinishedAt != 0 && run.Hosts[0].Status == "interrupted" {
			finished = true
			break
		}
	}
	if !finished {
		t.Fatal("finished status waited for output replay")
	}
	// Drain every retained page after observing completion, without losing output.
	for id := int64(1); id < 12; id++ {
		if err := wsjson.Write(ctx, output, map[string]any{"type": "output_ack", "id": id, "after": cursor}); err != nil {
			t.Fatal(err)
		}
		message := readTerminalMessage(t, output)
		if terminalMessageType(message) == "complete" {
			return
		}
		next := outputCursor(t, message)
		if next <= cursor {
			t.Fatal("output cursor did not advance")
		}
		cursor = next
	}
	t.Fatal("output replay did not complete")
}

func TestOutputChannelWaitsForAcknowledgementBeforeNextPage(t *testing.T) {
	f := newTerminalFixture(t)
	for range 40 {
		if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("packet\n")); err != nil {
			t.Fatal(err)
		}
	}
	conn := f.dialChannel(t, "output")
	cursor := outputCursor(t, readTerminalMessage(t, conn))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	next := make(chan map[string]json.RawMessage, 1)
	readError := make(chan error, 1)
	go func() {
		var message map[string]json.RawMessage
		if err := wsjson.Read(ctx, conn, &message); err != nil {
			readError <- err
			return
		}
		next <- message
	}()
	select {
	case <-next:
		t.Fatal("server queued output beyond the receive window")
	case err := <-readError:
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "output_ack", "id": 1, "after": cursor}); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-next:
		if outputCursor(t, message) <= cursor {
			t.Fatal("acknowledgement did not release next page")
		}
	case err := <-readError:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("acknowledged output was not streamed immediately")
	}
}

func TestFinalPartialOutputPageRequiresAcknowledgement(t *testing.T) {
	f := newTerminalFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.h.store.AppendOutput(ctx, f.run.ID, f.host.ID, []byte("final output")); err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := f.h.store.SetRunHost(ctx, f.run.ID, f.host.ID, "succeeded", "", &code); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.FinishRun(ctx, f.run.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	conn := f.dialChannel(t, "output")
	cursor := outputCursor(t, readTerminalMessage(t, conn))
	// A premature complete message would precede this history reply, exposing
	// completion before the client has acknowledged its final partial page.
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "history", "id": 1, "before": cursor}); err != nil {
		t.Fatal(err)
	}
	if kind := terminalMessageType(readTerminalMessage(t, conn)); kind != "history" {
		t.Fatalf("completion did not wait for the final output acknowledgement: %s", kind)
	}
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "output_ack", "id": 2, "after": cursor}); err != nil {
		t.Fatal(err)
	}
	if kind := terminalMessageType(readTerminalMessage(t, conn)); kind != "complete" {
		t.Fatalf("missing acknowledged completion: %s", kind)
	}
}

func TestOutputWakeupsCannotReleaseAnUnacknowledgedPage(t *testing.T) {
	f := newTerminalFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for range 80 {
		if err := f.h.store.AppendOutput(ctx, f.run.ID, f.host.ID, []byte("packet\n")); err != nil {
			t.Fatal(err)
		}
	}
	conn := f.dialChannel(t, "output")
	cursor := outputCursor(t, readTerminalMessage(t, conn))
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "output_ack", "id": 1, "after": cursor}); err != nil {
		t.Fatal(err)
	}
	second := outputCursor(t, readTerminalMessage(t, conn))
	if second <= cursor {
		t.Fatal("first acknowledgement did not advance output")
	}
	// Both output commits and final state are wakeups, not receive credit.
	if err := f.h.store.AppendOutput(ctx, f.run.ID, f.host.ID, []byte("final\n")); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.FinishRun(ctx, f.run.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	next := make(chan map[string]json.RawMessage, 1)
	readError := make(chan error, 1)
	go func() {
		var message map[string]json.RawMessage
		if err := wsjson.Read(ctx, conn, &message); err != nil {
			readError <- err
			return
		}
		next <- message
	}()
	select {
	case <-next:
		t.Fatal("a wakeup bypassed the pending acknowledgement")
	case err := <-readError:
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "output_ack", "id": 2, "after": second}); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-next:
		if outputCursor(t, message) <= second {
			t.Fatal("second acknowledgement did not release the next page")
		}
	case err := <-readError:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("output stalled after acknowledgement")
	}
}

func TestCompletedOutputChannelStillServesOlderHistory(t *testing.T) {
	f := newTerminalFixture(t)
	for range 80 {
		if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("packet\n")); err != nil {
			t.Fatal(err)
		}
	}
	chunks, _, err := f.h.store.RunOutputTail(context.Background(), f.actor, f.run.ID, f.host.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := f.h.store.SetRunHost(context.Background(), f.run.ID, f.host.ID, "succeeded", "", &code); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.FinishRun(context.Background(), f.run.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, f.path(chunks[len(chunks)-1].ID)+"&channel=output", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{f.h.origin}, "Cookie": []string{f.cookie.String()}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if terminalMessageType(readTerminalMessage(t, conn)) != "complete" {
		t.Fatal("completed output did not signal completion")
	}
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "history", "id": 1, "before": chunks[16].ID}); err != nil {
		t.Fatal(err)
	}
	message := readTerminalMessage(t, conn)
	var older []store.OutputChunk
	if err := json.Unmarshal(message["chunks"], &older); err != nil || terminalMessageType(message) != "history" || len(older) != 16 {
		t.Fatal("completed output channel lost older history")
	}
}

func TestSplitTerminalChannelsRejectWrongMessagesAndRevokedAcknowledgements(t *testing.T) {
	for _, scenario := range []struct {
		kind    string
		message map[string]any
	}{
		{"control", map[string]any{"type": "history", "id": 1, "before": 1}},
		{"control", map[string]any{"type": "output_ack", "id": 1, "after": 1}},
		{"output", map[string]any{"type": "input", "id": 1, "data": "private"}},
		{"output", map[string]any{"type": "output_ack", "id": 1, "after": 99999}},
	} {
		t.Run(scenario.kind+"/"+scenario.message["type"].(string), func(t *testing.T) {
			f := newTerminalFixture(t)
			if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("output")); err != nil {
				t.Fatal(err)
			}
			conn := f.dialChannel(t, scenario.kind)
			readTerminalMessage(t, conn)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := wsjson.Write(ctx, conn, scenario.message); err != nil {
				t.Fatal(err)
			}
			_, _, err := conn.Read(ctx)
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("invalid channel message accepted: %v", err)
			}
		})
	}
	f := newTerminalFixture(t)
	if err := f.h.store.AppendOutput(context.Background(), f.run.ID, f.host.ID, []byte("output")); err != nil {
		t.Fatal(err)
	}
	conn := f.dialChannel(t, "output")
	cursor := outputCursor(t, readTerminalMessage(t, conn))
	if err := f.h.store.DeleteSession(context.Background(), auth.TokenHash(f.cookie.Value)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "output_ack", "id": 1, "after": cursor}); err != nil {
		t.Fatal(err)
	}
	_, _, err := conn.Read(ctx)
	if websocket.CloseStatus(err) != 4001 {
		t.Fatalf("revoked acknowledgement was accepted: %v", err)
	}
}
