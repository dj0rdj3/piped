// Package terminal streams authenticated SSH terminal output and input.
package terminal

import (
	"sync"
	"time"

	"piped/internal/execution"
	"piped/internal/store"
)

type Handler struct {
	store          *store.Store
	executions     *execution.Engine
	origin         string
	allow          func(string, time.Time) bool
	terminalMu     sync.Mutex
	terminalCounts map[string]int
	terminalTotal  int
}

func New(db *store.Store, engine *execution.Engine, origin string, allow func(string, time.Time) bool) *Handler {
	return &Handler{store: db, executions: engine, origin: origin, allow: allow}
}
