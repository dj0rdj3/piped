package execution

import (
	"context"
	"errors"
	"time"

	"piped/internal/lifecycle"
	"piped/internal/store"
)

const persistenceTimeout = lifecycle.StorageTimeout

type hostResult struct {
	status  string
	message string
	code    *int
}

// Engine operations follow both the request and server lifetime. A database
// deadline is independent of HTTP write deadlines and SSH execution duration.
func (e *runtimeOwner) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return lifecycle.Operation(e.owner.Context(parent), e.ctx, persistenceTimeout)
}

func (e *Engine) setHostState(parent context.Context, runID, hostID, status, message string, code *int) error {
	ctx, cancel := context.WithTimeout(e.owner.Context(parent), persistenceTimeout)
	defer cancel()
	err := e.results.SetRunHost(ctx, runID, hostID, status, message, code)
	e.observeOwnership(err)
	return err
}

func (e *Engine) failHost(host *hostExecution, message string) {
	host.run.mu.Lock()
	defer host.run.mu.Unlock()
	if host.phase >= hostStopped {
		return
	}
	if host.failure == "" {
		host.failure = message
	}
	host.cancel()
}

func (e *Engine) persistHost(ctx context.Context, runID, jobName string, connection store.Connection, host *hostExecution) error {
	host.run.mu.Lock()
	if host.phase == hostPersisted {
		host.run.mu.Unlock()
		return nil
	}
	if host.phase != hostStopped {
		host.run.mu.Unlock()
		return ErrInactive
	}
	result := host.result
	host.run.mu.Unlock()
	if err := e.setHostState(ctx, runID, connection.Host.ID, result.status, result.message, result.code); err != nil {
		return err
	}
	host.run.mu.Lock()
	err := host.transition(hostPersisted)
	host.run.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case e.archiveReady <- struct{}{}:
	default:
	}
	if len(connection.Notifications) > 0 {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			e.notifier.NotifyHost(e.ctx, e.logger, runID, jobName, connection, result.status)
		}()
	}
	return nil
}

// Each completed host retries independently of other scripts and the parent.
// The common persistence context remains alive for a bounded shutdown drain.
func (e *Engine) retryPersistence(runID string, persist func(context.Context) error) bool {
	pending := false
	defer func() {
		if pending {
			e.pendingPersistence.Add(-1)
		}
	}()
	for {
		attempt, cancel := context.WithTimeout(e.persistenceCtx, persistenceTimeout)
		err := persist(attempt)
		cancel()
		e.observeOwnership(err)
		if err == nil {
			return true
		}
		if e.persistenceCtx.Err() != nil {
			e.logger.Error("execution finalization incomplete at shutdown; recovery required", "run", runID)
			return false
		}
		if !pending {
			pending = true
			e.pendingPersistence.Add(1)
		}
		e.logger.Error("execution finalization deferred; retrying persistence", "run", runID)
		timer := time.NewTimer(time.Second)
		select {
		case <-e.persistenceCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (e *runtimeOwner) loseOwnership() {
	e.ownershipLost.Store(true)
	e.cancel()
	e.stopPersistence()
}

func (e *runtimeOwner) observeOwnership(err error) {
	if errors.Is(err, store.ErrExecutionOwnershipLost) {
		e.loseOwnership()
	}
}

// Stop scripts first, including SSH cleanup and the masker's final output. Then
// permit one bounded persistence window for outstanding completion writes.
func (e *Engine) drainPersistence() {
	defer close(e.drainStopped)
	defer e.stopPersistence()
	<-e.ctx.Done()
	e.mu.Lock()
	e.mu.Unlock() // No later admission can add to scripts after cancellation.
	e.scripts.Wait()
	e.shutdown.Store(2)
	timer := time.NewTimer(persistenceTimeout)
	defer timer.Stop()
	select {
	case <-e.workersStopped:
	case <-timer.C:
	}
}

// Admission may have committed without its acknowledgement reaching us. It is
// never replayed: reconcile that exact ID as failed, including the no-row case.
// Keep the cleanup tracked until storage recovers or shutdown exhausts its drain.
func (e *Engine) reconcileAdmission(runID string) {
	_, cancel := context.WithCancel(e.ctx)
	controller := &runController{phase: runPersisting, hosts: map[string]*hostExecution{}, cancel: cancel}
	e.active[runID] = controller
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		saved := e.retryPersistence(runID, func(ctx context.Context) error {
			return e.results.FailUnlaunchedRun(ctx, runID)
		})
		controller.mu.Lock()
		if saved {
			controller.phase = runPersisted
		} else {
			controller.phase = runRecoveryRequired
		}
		controller.mu.Unlock()
		if saved {
			e.mu.Lock()
			delete(e.active, runID)
			e.mu.Unlock()
		}
	}()
}

// A lost SQL session may precede the old controller's heartbeat detection and
// remote cleanup. Fence it immediately, then leave time for both bounded waits
// before recovering its history and allowing replacement executions.
func waitForTakeover(ctx context.Context, owner *store.ExecutionOwner) error {
	timer := time.NewTimer(lifecycle.OwnershipGrace)
	defer timer.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-ticker.C:
			check, cancel := context.WithTimeout(ctx, persistenceTimeout)
			err := owner.Check(check)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}
