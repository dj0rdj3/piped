package terminal

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"piped/internal/store"
)

type terminalChannel struct {
	kind         string
	pending      atomic.Int64
	acknowledged chan struct{}
}

// Control messages have their own connection, so output replay cannot queue in
// front of input acknowledgements or status. Output allows one bounded page in
// flight; SQLite holds any backlog instead of browser/network message queues.
func (s *Handler) streamTerminal(ctx context.Context, conn *websocket.Conn, actor store.Actor, runID, hostID string, after int64, changes, inputDone <-chan struct{}, inputs *atomic.Int64, channel *terminalChannel) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	completeSent := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.executions.Done():
			return nil
		default:
		}
		if channel.pending.Load() == 0 {
			more, finished, err := s.pushTerminal(ctx, conn, actor, runID, hostID, &after, channel)
			if err != nil {
				return err
			}
			if finished && !more && inputs.Load() == 0 && !completeSent {
				if err := writeTerminal(ctx, conn, map[string]any{"type": "complete"}); err != nil {
					return err
				}
				completeSent = true
			}
			if more {
				continue
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.executions.Done():
			return nil
		case <-channel.acknowledged:
		case <-changes:
		case <-inputDone:
		case <-ticker.C:
			if err := s.store.Authorize(ctx, actor, store.ScopeRunsRead); err != nil {
				return err
			}
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}
