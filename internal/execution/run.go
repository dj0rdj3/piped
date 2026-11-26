package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/crypto/ssh"
	"piped/internal/store"
)

type hostPhase uint8

const (
	hostQueued hostPhase = iota
	hostConnecting
	hostAwaitingApproval
	hostRunning
	hostStopped
	hostPersisted
)

type runPhase uint8

const (
	runAdmitted runPhase = iota
	runExecuting
	runPersisting
	runPersisted
	runRecoveryRequired
)

type hostExecution struct {
	run      *runController
	ctx      context.Context
	cancel   context.CancelFunc
	phase    hostPhase
	result   hostResult
	session  remoteSession
	pending  *HostKey
	decision chan bool
	failure  string
}

// A controller owns one run's mutable state. Engine.mu only protects admission,
// scheduling and the registry; a blocked control operation cannot lock other runs.
// Host execution and durable completion are distinct, monotonic lifecycle events.
type runController struct {
	mu            controlLock
	phase         runPhase
	hosts         map[string]*hostExecution
	cancel        context.CancelFunc
	stopOnFailure bool
}

// transition is called under the controller mutex. Repeated acknowledgements of
// the same phase are harmless; terminal phases can never become live again.
func (host *hostExecution) transition(next hostPhase) error {
	allowed := host.phase == next
	switch host.phase {
	case hostQueued:
		allowed = allowed || next == hostConnecting || next == hostStopped
	case hostConnecting:
		allowed = allowed || next == hostAwaitingApproval || next == hostRunning || next == hostStopped
	case hostAwaitingApproval:
		allowed = allowed || next == hostConnecting || next == hostStopped
	case hostRunning:
		allowed = allowed || next == hostStopped
	case hostStopped:
		allowed = allowed || next == hostPersisted
	}
	if !allowed {
		return ErrInactive
	}
	host.phase = next
	return nil
}

func (run *runController) host(hostID string) (*hostExecution, error) {
	host := run.hosts[hostID]
	if host == nil || host.ctx.Err() != nil || host.phase >= hostStopped {
		return nil, ErrInactive
	}
	return host, nil
}

// launch runs under the admission mutex after a durable transaction. There is
// exactly one controller and one script execution per host for this admission.
func (e *Engine) launch(run store.Run, job store.Job, connections []store.Connection) store.Run {
	runContext, cancel := context.WithCancel(e.ctx)
	controller := &runController{
		phase: runAdmitted, hosts: make(map[string]*hostExecution, len(connections)),
		cancel: cancel, stopOnFailure: job.StopOnFailure,
	}
	for _, connection := range connections {
		ctx, cancel := context.WithCancel(runContext)
		controller.hosts[connection.Host.ID] = &hostExecution{run: controller, ctx: ctx, cancel: cancel}
	}
	e.active[run.ID] = controller
	e.scripts.Add(1)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		controller.execute(e, run.ID, job, connections)
	}()
	return run
}

func (run *runController) execute(e *Engine, runID string, job store.Job, connections []store.Connection) {
	defer run.cancel()
	run.mu.Lock()
	run.phase = runExecuting
	run.mu.Unlock()
	var scripts, persistence sync.WaitGroup
	for _, connection := range connections {
		execute := func() {
			host := run.hosts[connection.Host.ID]
			run.executeHost(e, runID, job, connection, host)
			persistence.Add(1)
			go func() {
				defer persistence.Done()
				e.retryPersistence(runID, func(ctx context.Context) error {
					return e.persistHost(ctx, runID, job.Name, connection, host)
				})
			}()
		}
		if job.ExecutionMode == "parallel" {
			scripts.Add(1)
			go func() { defer scripts.Done(); execute() }()
		} else {
			execute()
		}
	}
	scripts.Wait()
	run.mu.Lock()
	run.phase = runPersisting
	run.mu.Unlock()
	e.scripts.Done() // Remote cleanup and final masked output have joined.
	persistence.Wait()
	run.mu.Lock()
	status := "succeeded"
	for _, host := range run.hosts {
		if host.phase != hostPersisted {
			run.phase = runRecoveryRequired
			run.mu.Unlock()
			return
		}
		status = combineStatus(status, host.result.status)
	}
	run.mu.Unlock()
	saved := e.retryPersistence(runID, func(ctx context.Context) error {
		return e.results.FinishRun(ctx, runID, status)
	})
	run.mu.Lock()
	if saved {
		run.phase = runPersisted
	} else {
		run.phase = runRecoveryRequired
	}
	run.mu.Unlock()
	if saved {
		e.mu.Lock()
		delete(e.active, runID)
		e.mu.Unlock()
		select {
		case e.archiveReady <- struct{}{}:
		default:
		}
	}
}

func combineStatus(current, next string) string {
	for _, status := range []string{"failed", "interrupted", "cancelled"} {
		if current == status || next == status {
			return status
		}
	}
	return "succeeded"
}

func (run *runController) executeHost(e *Engine, runID string, job store.Job, connection store.Connection, host *hostExecution) {
	var code *int
	var err error
	status, message := "succeeded", ""
	if host.ctx.Err() == nil {
		run.mu.Lock()
		err = host.transition(hostConnecting)
		run.mu.Unlock()
		if err == nil {
			err = e.setHostState(host.ctx, runID, connection.Host.ID, "connecting", "", nil)
		}
		if err == nil {
			code, err = e.runHost(host, connection, job.Script, &outputWriter{engine: e, runID: runID, hostID: connection.Host.ID, host: host}, func() {
				run.mu.Lock()
				transitionErr := host.transition(hostRunning)
				run.mu.Unlock()
				if transitionErr != nil {
					e.failHost(host, "Execution state could not be saved")
					return
				}
				if updateErr := e.setHostState(host.ctx, runID, connection.Host.ID, "running", "", nil); updateErr != nil && host.ctx.Err() == nil {
					e.failHost(host, "Execution state could not be saved")
				}
			}, func(key ssh.PublicKey) error { return e.checkHostKey(host, connection.Host, key, runID) })
		}
		if err != nil {
			status, message = "failed", "SSH execution failed; check the host connection, credentials, sudo, and remote bash support"
			if code != nil {
				message = fmt.Sprintf("Script exited with status %d", *code)
				if *code == 130 {
					status, message = "interrupted", "Script interrupted (Ctrl+C)"
				}
			}
			if errors.Is(err, ErrHostKeyDenied) {
				message = ErrHostKeyDenied.Error()
			}
		}
	}
	run.mu.Lock()
	if host.ctx.Err() != nil {
		status, message = "cancelled", "Execution cancelled"
	}
	if host.failure != "" {
		status, message, code = "failed", host.failure, nil
	}
	host.session, host.pending = nil, nil
	host.cancel()
	if err := host.transition(hostStopped); err != nil {
		run.mu.Unlock()
		e.logger.Error("host execution attempted a terminal state transition", "run", runID, "host", connection.Host.ID)
		run.cancel()
		return
	}
	host.result = hostResult{status: status, message: message, code: code}
	if status != "succeeded" && run.stopOnFailure {
		run.cancel()
	}
	run.mu.Unlock()
	if status == "failed" {
		if code != nil {
			e.logger.Warn("script exited", "run", runID, "host", connection.Host.ID, "exit_code", *code)
		} else {
			e.logger.Warn("host execution failed", "run", runID, "host", connection.Host.ID)
		}
	}
}
