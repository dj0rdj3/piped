package execution

import (
	"context"

	"piped/internal/store"
)

// RuntimeStatus is deliberately aggregate metadata. Never add identities,
// addresses, SQL errors, scripts, keys or connection settings to this payload.
type RuntimeStatus struct {
	Ready               bool   `json:"ready"`
	ActiveRuns          int    `json:"active_runs"`
	ActiveScripts       int    `json:"active_scripts"`
	BusyRuns            int    `json:"busy_runs"`
	PendingResults      int    `json:"pending_results"`
	RetryingPersistence int64  `json:"retrying_persistence"`
	OwnershipLost       bool   `json:"ownership_lost"`
	Shutdown            string `json:"shutdown"`
}

// Ready is a constant-time cached probe. Anonymous probes cannot consume pooled
// database connections or contend with admission and shutdown locks.
func (e *Engine) Ready() bool {
	if e == nil || e.ctx.Err() != nil || e.ownershipLost.Load() || e.shutdown.Load() != 0 || e.pendingPersistence.Load() != 0 {
		return false
	}
	select {
	case <-e.owner.Lost():
		return false
	default:
		return true
	}
}

func (e *Engine) Runtime(ctx context.Context, actor store.Actor) (RuntimeStatus, error) {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if err := e.authority.Authorize(ctx, actor, store.ScopeRunsRead); err != nil {
		return RuntimeStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return RuntimeStatus{}, err
	}
	return e.runtimeStatus(), nil
}

func (e *Engine) runtimeStatus() RuntimeStatus {
	status := RuntimeStatus{Ready: e.Ready(), OwnershipLost: e.ownershipLost.Load(), RetryingPersistence: e.pendingPersistence.Load()}
	phases := [...]string{"running", "stopping_scripts", "draining_results", "stopped"}
	status.Shutdown = phases[e.shutdown.Load()]
	e.mu.Lock()
	controllers := make([]*runController, 0, len(e.active))
	for _, run := range e.active {
		controllers = append(controllers, run)
	}
	e.mu.Unlock()
	status.ActiveRuns = len(controllers)
	for _, run := range controllers {
		// Observability must not hold the admission lock while waiting for a host
		// approval or input operation. Report busy controllers explicitly instead.
		if !run.mu.TryLock() {
			status.BusyRuns++
			continue
		}
		pending := false
		for _, host := range run.hosts {
			switch {
			case host.phase > hostQueued && host.phase < hostStopped:
				status.ActiveScripts++
			case host.phase == hostStopped:
				status.PendingResults++
				pending = true
			}
		}
		if !pending && (run.phase == runPersisting || run.phase == runRecoveryRequired) {
			status.PendingResults++
		}
		run.mu.Unlock()
	}
	return status
}
