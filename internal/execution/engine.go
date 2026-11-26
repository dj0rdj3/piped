// Package execution coordinates persistent job runs and interactive SSH sessions.
package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/notifications"
	"piped/internal/secrets"
	"piped/internal/store"
)

var (
	ErrInactive      = errors.New("execution is no longer active")
	ErrInput         = errors.New("invalid execution request")
	ErrApproval      = errors.New("host-key request changed; refresh and try again")
	ErrHostKeyDenied = errors.New("SSH host key denied")
	ErrOwnershipLost = store.ErrExecutionOwnershipLost
)

type HostKey struct {
	HostID              string `json:"host_id"`
	Address             string `json:"address"`
	Port                int    `json:"port"`
	PublicKey           string `json:"public_key"`
	Fingerprint         string `json:"fingerprint"`
	PreviousFingerprint string `json:"previous_fingerprint"`
}

type hostExecution struct {
	ctx      context.Context
	cancel   context.CancelFunc
	input    io.WriteCloser
	pending  *HostKey
	decision chan bool
	writeMu  sync.Mutex
	failure  string // Guarded by Engine.mu; contains only a public diagnostic.
}

type activeRun struct {
	hosts         map[string]*hostExecution
	cancel        context.CancelFunc
	stopOnFailure bool
}

type Engine struct {
	owner               *store.ExecutionOwner
	ownershipLost       atomic.Bool
	closeOnce           sync.Once
	persistenceCtx      context.Context
	stopPersistence     context.CancelFunc
	scripts             sync.WaitGroup
	workersStopped      chan struct{}
	drainStopped        chan struct{}
	notifier            *notifications.Service
	db                  *store.Store
	vault               *secrets.Vault
	logger              *slog.Logger
	ctx                 context.Context
	cancel              context.CancelFunc
	mu                  sync.Mutex
	active              map[string]*activeRun
	wg                  sync.WaitGroup
	archiveReady        chan struct{}
	maintenanceEstimate time.Duration
	maintenanceMu       sync.Mutex
	maintenanceCancel   context.CancelFunc
	manualAdmissions    int
	now                 func() time.Time
	schedulerStarted    bool
	scheduleMinute      int64
	schedulePending     int // Guarded by mu; count overlapping batches, not just the newest minute.
	runner              func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error)
}

func New(ctx context.Context, db *store.Store, vault *secrets.Vault, logger *slog.Logger, retentionDays int) (*Engine, error) {
	if vault == nil || retentionDays < 1 || retentionDays > 36500 {
		return nil, ErrInput
	}
	if logger == nil {
		logger = slog.Default()
	}
	owner, err := db.AcquireExecutionOwner(ctx)
	if err != nil {
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized {
			owner.Close()
		}
	}()
	setup, stopSetup := context.WithTimeout(ctx, persistenceTimeout)
	notifier, err := notifications.New(setup, db, vault)
	stopSetup()
	if err != nil {
		return nil, err
	}
	reclaimer, err := db.NewSQLiteReclaimer(ctx)
	if err != nil {
		return nil, fmt.Errorf("prepare SQLite space reclamation: %w", err)
	}
	takeover, err := owner.Claim(ctx)
	if err == nil && takeover {
		err = waitForTakeover(ctx, owner)
	}
	if err != nil {
		if reclaimer != nil {
			reclaimer.Close()
		}
		return nil, err
	}
	recovery, stopRecovery := context.WithTimeout(ctx, persistenceTimeout)
	err = db.RecoverRuns(owner.Context(recovery))
	stopRecovery()
	if err != nil {
		if reclaimer != nil {
			reclaimer.Close()
		}
		return nil, err
	}
	ctx, cancel := context.WithCancel(owner.Context(ctx))
	persistenceCtx, stopPersistence := context.WithCancel(context.WithoutCancel(ctx))
	engine := &Engine{
		persistenceCtx:      persistenceCtx,
		stopPersistence:     stopPersistence,
		workersStopped:      make(chan struct{}),
		drainStopped:        make(chan struct{}),
		owner:               owner,
		notifier:            notifier,
		db:                  db,
		vault:               vault,
		logger:              logger,
		ctx:                 ctx,
		cancel:              cancel,
		active:              map[string]*activeRun{},
		archiveReady:        make(chan struct{}, 1),
		maintenanceEstimate: store.ServerReclamationBudget,
		now:                 time.Now,
	}
	engine.runner = engine.runSSH
	go engine.drainPersistence()
	engine.wg.Add(1)
	go func() {
		defer engine.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-owner.Lost():
				engine.loseOwnership()
				return
			case <-ticker.C:
				check, stop := context.WithTimeout(ctx, 5*time.Second)
				err := owner.Check(check)
				stop()
				if err != nil {
					if ctx.Err() == nil {
						logger.Error("execution ownership lost; stopping server")
						engine.loseOwnership()
					}
					cancel()
					return
				}
			}
		}
	}()
	if reclaimer != nil {
		engine.wg.Add(1)
		go func() {
			defer engine.wg.Done()
			defer reclaimer.Close()
			timer := time.NewTimer(0)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				more, err := reclaimer.Step(ctx)
				delay := time.Second
				if err != nil && ctx.Err() == nil {
					logger.Error("SQLite space reclamation failed", "error", err)
					delay = time.Minute
				} else if more {
					delay = 50 * time.Millisecond
				}
				timer.Reset(delay)
			}
		}()
	}
	engine.wg.Add(2)
	go func() {
		defer engine.wg.Done()
		retry := time.NewTicker(time.Minute)
		defer retry.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-engine.archiveReady:
			case <-retry.C:
			}
			compression, cancel := context.WithTimeout(ctx, persistenceTimeout)
			err := db.CompressCompletedOutput(compression)
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error("execution log compression failed", "error", err)
			}
			engine.reclaimServerOutput()

		}
	}()
	go func() {
		defer engine.wg.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			retention, cancel := context.WithTimeout(ctx, persistenceTimeout)
			err := db.PurgeRuns(retention, time.Now().AddDate(0, 0, -retentionDays).Unix())
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error("execution retention failed", "error", err)
			}
			// A single worker drains persisted completions. Coalesce wakeups rather
			// than allocating another goroutine or queued ID for every finished host.
			select {
			case engine.archiveReady <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	initialized = true
	return engine, nil
}

// Done lets long-lived terminal connections follow service shutdown.
func (e *Engine) Done() <-chan struct{} { return e.ctx.Done() }

// Err distinguishes a lost ownership session from a normal signal or shutdown.
func (e *Engine) Err() error {
	if e.ownershipLost.Load() {
		return ErrOwnershipLost
	}
	return nil
}

func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		// Cancel maintenance before waiting for its admission lock. Retain
		// database ownership until remote cleanup and finalization have joined.
		e.cancel()
		e.mu.Lock()
		e.mu.Unlock()
		e.wg.Wait()
		close(e.workersStopped)
		<-e.drainStopped
		e.mu.Lock()
		pending := len(e.active) > 0
		e.mu.Unlock()
		if pending || e.ownershipLost.Load() {
			// Failed persistence has already exhausted its drain. Do not add
			// more database round trips while releasing an unhealthy session.
			if err := e.owner.Discard(); err != nil {
				e.logger.Error("discard execution ownership failed")
			}
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
		if err := e.owner.MarkStopped(cleanup); err != nil && !e.ownershipLost.Load() {
			e.logger.Error("clear execution ownership generation failed")
		}
		cancel()
		if err := e.owner.Close(); err != nil {
			e.logger.Error("release execution ownership failed")
		}
	})
}

func (e *Engine) Start(ctx context.Context, actor store.Actor, jobID string, version int64) (store.Run, error) {
	if e.ctx.Err() != nil {
		return store.Run{}, ErrInactive
	}
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if version < 1 {
		return store.Run{}, ErrInput
	}
	// Validate authority before allowing a request to interrupt database work.
	// CreateRun checks it again transactionally when admission is granted.
	if err := e.db.Authorize(ctx, actor, store.ScopeRunsWrite); err != nil {
		e.observeOwnership(err)
		return store.Run{}, err
	}
	e.maintenanceMu.Lock()
	e.manualAdmissions++
	if e.maintenanceCancel != nil {
		e.maintenanceCancel()
	}
	e.maintenanceMu.Unlock()
	defer func() {
		e.maintenanceMu.Lock()
		e.manualAdmissions--
		e.maintenanceMu.Unlock()
	}()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil {
		return store.Run{}, ErrInactive
	}
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	run, job, connections, err := e.db.CreateRun(ctx, actor, hex.EncodeToString(random), jobID, version)
	if err != nil {
		e.observeOwnership(err)
		if run.ID != "" && !e.ownershipLost.Load() {
			e.reconcileAdmission(run.ID)
		}
		return store.Run{}, err
	}
	return e.launch(run, job, connections), nil
}

// launch is called with e.mu held after a run has been transactionally admitted.
func (e *Engine) launch(run store.Run, job store.Job, connections []store.Connection) store.Run {
	runContext, cancel := context.WithCancel(e.ctx)
	active := &activeRun{hosts: map[string]*hostExecution{}, cancel: cancel, stopOnFailure: job.StopOnFailure}
	for _, c := range connections {
		ctx, cancel := context.WithCancel(runContext)
		active.hosts[c.Host.ID] = &hostExecution{ctx: ctx, cancel: cancel}
	}
	e.active[run.ID] = active
	e.scripts.Add(1)
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.execute(run.ID, job, connections, active) }()
	return run
}

func (e *Engine) execute(runID string, job store.Job, connections []store.Connection, active *activeRun) {
	defer active.cancel()
	var wg sync.WaitGroup
	var persistence sync.WaitGroup
	// Each worker owns one result slot. Wait before reading the completed list;
	// result collection needs no shared flags or additional mutex.
	results := make([]hostResult, len(connections))
	saved := make([]bool, len(connections))
	executeHost := func(index int, c store.Connection) {
		host := active.hosts[c.Host.ID]
		var code *int
		var err error
		status, message := "succeeded", ""
		if host.ctx.Err() == nil {
			if err = e.setHostState(host.ctx, runID, c.Host.ID, "connecting", "", nil); err == nil {
				code, err = e.runHost(host, c, job.Script, &outputWriter{engine: e, runID: runID, hostID: c.Host.ID, host: host}, func(input io.WriteCloser) {
					e.mu.Lock()
					host.input = input
					e.mu.Unlock()
					if updateErr := e.setHostState(host.ctx, runID, c.Host.ID, "running", "", nil); updateErr != nil && host.ctx.Err() == nil {
						e.failHost(host, "Execution state could not be saved")
					}
				}, func(key ssh.PublicKey) error { return e.checkHostKey(host, c.Host, key, runID) })
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
		e.mu.Lock()
		if host.ctx.Err() != nil {
			status, message = "cancelled", "Execution cancelled"
		}
		if host.failure != "" {
			status, message, code = "failed", host.failure, nil
		}
		host.input, host.pending = nil, nil
		host.cancel()
		if (status == "failed" || status == "cancelled" || status == "interrupted") && job.StopOnFailure {
			active.cancel()
		}
		e.mu.Unlock()
		if status == "failed" {
			if code != nil {
				e.logger.Warn("script exited", "run", runID, "host", c.Host.ID, "exit_code", *code)
			} else {
				e.logger.Warn("host execution failed", "run", runID, "host", c.Host.ID)
			}
		}
		results[index] = hostResult{status: status, message: message, code: code}
		persistence.Add(1)
		go func() {
			defer persistence.Done()
			result := results[index]
			saved[index] = e.retryPersistence(runID, func(ctx context.Context) error {
				return e.persistHost(ctx, runID, job.Name, c, &result)
			})
		}()
	}
	for index, connection := range connections {
		if job.ExecutionMode == "parallel" {
			wg.Add(1)
			go func() { defer wg.Done(); executeHost(index, connection) }()
		} else {
			executeHost(index, connection)
		}
	}
	wg.Wait()
	e.scripts.Done()
	persistence.Wait()
	if slices.Contains(saved, false) {
		return
	}
	status := "succeeded"
	for _, outcome := range []string{"failed", "interrupted", "cancelled"} {
		if slices.ContainsFunc(results, func(result hostResult) bool { return result.status == outcome }) {
			status = outcome
			break
		}
	}
	if !e.retryPersistence(runID, func(ctx context.Context) error {
		return e.db.FinishRun(ctx, runID, status)
	}) {
		return
	}
	e.mu.Lock()
	delete(e.active, runID)
	e.mu.Unlock()
	select {
	case e.archiveReady <- struct{}{}:
	default:
	}
}

type outputWriter struct {
	engine        *Engine
	runID, hostID string
	host          *hostExecution
}

func (w *outputWriter) Write(data []byte) (int, error) {
	// Persist bounded transactions without waiting on a batching timer. The store
	// splits each transaction into small cursor chunks for history reads.
	written := 0
	for len(data) > 0 {
		size := min(len(data), store.MaxOutputBatchBytes)
		ctx, cancel := context.WithTimeout(w.engine.persistenceCtx, persistenceTimeout)
		err := w.engine.db.AppendOutput(ctx, w.runID, w.hostID, data[:size])
		cancel()
		if err != nil {
			w.engine.observeOwnership(err)
			if w.engine.ctx.Err() == nil {
				w.engine.failHost(w.host, "Execution output could not be saved")
			}
			return written, err
		}
		written += size
		data = data[size:]
	}
	return written, nil
}

func (e *Engine) host(runID, hostID string) (*hostExecution, error) {
	run := e.active[runID]
	if run == nil {
		return nil, ErrInactive
	}
	host := run.hosts[hostID]
	if host == nil || host.ctx.Err() != nil {
		return nil, ErrInactive
	}
	return host, nil
}

func (e *Engine) Cancel(ctx context.Context, actor store.Actor, runID, hostID string) error {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if err := e.db.Authorize(ctx, actor, store.ScopeRunsWrite); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	host, err := e.host(runID, hostID)
	if err != nil {
		return err
	}
	host.cancel()
	if run := e.active[runID]; run.stopOnFailure {
		run.cancel()
	}
	return nil
}

func (e *Engine) Input(ctx context.Context, actor store.Actor, runID, hostID, data string) error {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if len(data) == 0 || len(data) > 4096 {
		return ErrInput
	}
	if err := e.db.Authorize(ctx, actor, store.ScopeRunsWrite); err != nil {
		return err
	}
	e.mu.Lock()
	host, err := e.host(runID, hostID)
	var input io.WriteCloser
	if err == nil {
		input = host.input
	}
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if input == nil {
		return ErrInactive
	}
	// A bounded write deadline prevents a host that stops reading from pinning
	// an HTTP handler. Once input is admitted, browser disconnection does not
	// cancel delivery or stop the run. Successful delivery can finish the script
	// immediately, so its completion must not be mistaken for a failed write.
	host.writeMu.Lock()
	defer host.writeMu.Unlock()
	done := make(chan error, 1)
	go func() {
		count, err := io.WriteString(input, data)
		if err == nil && count != len(data) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil && host.ctx.Err() != nil {
			return ErrInactive
		}
		return err
	case <-timer.C:
		e.failHost(host, "Terminal input timed out")
		return ErrInactive
	}
}

func (e *Engine) Pending(ctx context.Context, actor store.Actor, runID string) ([]HostKey, error) {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if err := e.db.Authorize(ctx, actor, store.ScopeRunsRead); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	keys := []HostKey{}
	if run := e.active[runID]; run != nil {
		for _, host := range run.hosts {
			if host.pending != nil {
				keys = append(keys, *host.pending)
			}
		}
	}
	return keys, nil
}

func (e *Engine) Approve(ctx context.Context, actor store.Actor, runID, hostID, fingerprint string, accept bool) error {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	// Denial needs execution authority too; trust changes require host authority.
	if err := e.db.Authorize(ctx, actor, store.ScopeRunsWrite); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	host, err := e.host(runID, hostID)
	if err != nil {
		return err
	}
	if host.pending == nil || host.pending.Fingerprint != fingerprint {
		return ErrApproval
	}
	key := host.pending
	if accept {
		if err := e.db.ApproveHostKey(ctx, actor, hostID, key.Address, key.Port, key.PublicKey); err != nil {
			return err
		}
	}
	host.pending = nil
	host.decision <- accept
	return nil
}

func (e *Engine) checkHostKey(host *hostExecution, target store.Host, key ssh.PublicKey, runID string) error {
	public := string(ssh.MarshalAuthorizedKey(key))
	ctx, cancel := e.operationContext(host.ctx)
	trusted, err := e.db.TrustedHostKey(ctx, target.ID, target.Address, target.Port)
	cancel()
	if err != nil {
		return err
	}
	if trusted == public {
		return nil
	}
	request := &HostKey{HostID: target.ID, Address: target.Address, Port: target.Port, PublicKey: public, Fingerprint: ssh.FingerprintSHA256(key)}
	if previous, _, _, _, err := ssh.ParseAuthorizedKey([]byte(trusted)); err == nil {
		request.PreviousFingerprint = ssh.FingerprintSHA256(previous)
	}
	e.mu.Lock()
	host.pending, host.decision = request, make(chan bool, 1)
	decision := host.decision
	e.mu.Unlock()
	if err := e.setHostState(host.ctx, runID, target.ID, "awaiting_approval", "SSH host key requires approval", nil); err != nil {
		return err
	}
	select {
	case accept := <-decision:
		if !accept {
			return ErrHostKeyDenied
		}
		return nil
	case <-host.ctx.Done():
		return host.ctx.Err()
	}
}

// Decrypt the admitted snapshot only for the lifetime of this host execution.
// Every runner, including failures and stderr, passes through masking first.
func (e *Engine) runHost(host *hostExecution, c store.Connection, script string, output io.Writer, ready func(io.WriteCloser), verify func(ssh.PublicKey) error) (*int, error) {
	for _, secret := range c.JobSecrets {
		plain, err := e.vault.Open("job:"+c.JobID+":env:"+secret.Name, secret.Ciphertext)
		if err != nil {
			return nil, err
		}
		defer clear(plain)
		if !secrets.ValidVariableName(secret.Name) {
			return nil, errors.New("invalid stored job secret name")
		}
		c.Environment = append(c.Environment, secrets.Variable{Name: secret.Name, Value: plain})
	}
	writer, err := newSecretWriter(output, c.Environment)
	if err != nil {
		return nil, err
	}
	code, runErr := e.runner(host.ctx, c, script, writer, ready, verify)
	if err := writer.Close(); err != nil && runErr == nil {
		runErr = err
	}
	return code, runErr
}
