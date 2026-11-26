package execution

import (
	"context"
	"errors"
	"time"

	"piped/internal/store"
)

const persistenceTimeout = 5 * time.Second

type hostResult struct {
	status    string
	message   string
	code      *int
	persisted bool
}

// Engine operations follow both the request and server lifetime. A database
// deadline is independent of HTTP write deadlines and SSH execution duration.
func (e *Engine) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(e.owner.Context(parent), persistenceTimeout)
	stop := context.AfterFunc(e.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (e *Engine) setHostState(parent context.Context, runID, hostID, status, message string, code *int) error {
	ctx, cancel := context.WithTimeout(e.owner.Context(parent), persistenceTimeout)
	defer cancel()
	err := e.db.SetRunHost(ctx, runID, hostID, status, message, code)
	e.observeOwnership(err)
	return err
}

func (e *Engine) failHost(host *hostExecution, message string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if host.failure == "" {
		host.failure = message
	}
	host.cancel()
}

func (e *Engine) persistHost(ctx context.Context, runID, jobName string, connection store.Connection, result *hostResult) error {
	if result.persisted {
		return nil
	}
	if err := e.setHostState(ctx, runID, connection.Host.ID, result.status, result.message, result.code); err != nil {
		return err
	}
	result.persisted = true
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
		e.logger.Error("execution finalization deferred; retrying persistence", "run", runID)
		timer := time.NewTimer(time.Second)
		select {
		case <-e.persistenceCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (e *Engine) loseOwnership() {
	e.ownershipLost.Store(true)
	e.cancel()
	e.stopPersistence()
}

func (e *Engine) observeOwnership(err error) {
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
	e.active[runID] = &activeRun{hosts: map[string]*hostExecution{}, cancel: cancel}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		if e.retryPersistence(runID, func(ctx context.Context) error {
			return e.db.FailUnlaunchedRun(ctx, runID)
		}) {
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
	timer := time.NewTimer(2*persistenceTimeout + 2*time.Second)
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
