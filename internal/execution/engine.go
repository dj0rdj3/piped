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
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/notifications"
	"piped/internal/problem"
	"piped/internal/secrets"
	"piped/internal/store"
)

var (
	ErrInactive      = problem.New(problem.Conflict, "execution is no longer active")
	ErrInput         = problem.New(problem.Invalid, "invalid execution request")
	ErrApproval      = problem.New(problem.Conflict, "host-key request changed; refresh and try again")
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

type Engine struct {
	*runtimeOwner
	notifier            *notifications.Service
	db                  *store.Store
	admissions          admissionStorage
	authority           commandAuthority
	results             resultStorage
	vault               *secrets.Vault
	mu                  controlLock
	active              map[string]*runController
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
	runner              sshRunner
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
	runtime := &runtimeOwner{
		owner: owner, ctx: ctx, cancel: cancel, logger: logger,
		persistenceCtx: persistenceCtx, stopPersistence: stopPersistence,
		workersStopped: make(chan struct{}), drainStopped: make(chan struct{}),
	}
	engine := &Engine{
		runtimeOwner:        runtime,
		notifier:            notifier,
		db:                  db,
		admissions:          db,
		authority:           db,
		results:             db,
		vault:               vault,
		active:              map[string]*runController{},
		archiveReady:        make(chan struct{}, 1),
		maintenanceEstimate: store.ServerReclamationBudget,
		now:                 time.Now,
	}
	engine.runner = engine.runSSH
	go engine.drainPersistence()
	runtime.startMonitor(&engine.wg)
	maintenance := &maintenanceWorker{db: db, archiveReady: engine.archiveReady, retentionDays: retentionDays, reclaim: engine.reclaimServerOutput}
	maintenance.start(ctx, logger, reclaimer, &engine.wg)
	initialized = true
	return engine, nil
}

// Done lets long-lived terminal connections follow service shutdown.
func (e *runtimeOwner) Done() <-chan struct{} { return e.ctx.Done() }

// Err distinguishes a lost ownership session from a normal signal or shutdown.
func (e *runtimeOwner) Err() error {
	if e.ownershipLost.Load() {
		return ErrOwnershipLost
	}
	return nil
}

func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		e.shutdown.Store(1)
		defer e.shutdown.Store(3)
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
		e.release(pending)
	})
}

type StartRequest struct {
	JobID     string `json:"job_id"`
	Version   int64  `json:"version"`
	RequestID string `json:"request_id"`
}

func (e *Engine) Start(ctx context.Context, actor store.Actor, input StartRequest) (store.Run, error) {
	if e.ctx.Err() != nil {
		return store.Run{}, ErrInactive
	}
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if _, err := parseResourceID(input.JobID); err != nil {
		return store.Run{}, err
	}
	if !store.ValidAdmissionKey(input.RequestID) {
		return store.Run{}, store.ErrAdmissionKey
	}
	if input.Version < 1 {
		return store.Run{}, ErrInput
	}
	// Validate authority before allowing a request to interrupt database work.
	// AdmitRun checks it again transactionally when admission is granted.
	if err := e.authority.Authorize(ctx, actor, store.ScopeRunsWrite); err != nil {
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
	if err := e.mu.LockContext(ctx); err != nil {
		return store.Run{}, err
	}
	defer e.mu.Unlock()
	if e.ctx.Err() != nil {
		return store.Run{}, ErrInactive
	}
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	runID := hex.EncodeToString(random)
	admitted, err := e.admissions.AdmitRun(ctx, actor, store.RunAdmission{
		ID: runID, JobID: input.JobID, Version: input.Version, RequestID: input.RequestID,
	})
	if err != nil {
		e.observeOwnership(err)
		var uncertain *store.UncertainCommit
		if errors.As(err, &uncertain) && !e.ownershipLost.Load() {
			e.reconcileAdmission(uncertain.RunID)
		}
		return store.Run{}, err
	}
	switch result := admitted.(type) {
	case store.ExistingRun:
		return result.Run, nil
	case store.CreatedRun:
		return e.launch(result.Run, result.Job, result.Connections), nil
	default:
		return store.Run{}, errors.New("invalid admission outcome")
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
		err := w.engine.results.AppendOutput(ctx, w.runID, w.hostID, data[:size])
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

func (e *Engine) findRun(ctx context.Context, runID string) (*runController, error) {
	id, err := parseResourceID(runID)
	if err != nil {
		return nil, err
	}
	if err := e.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	run := e.active[id.value]
	e.mu.Unlock()
	if run == nil {
		return nil, ErrInactive
	}
	return run, nil
}

func (e *Engine) Cancel(ctx context.Context, actor store.Actor, request HostRequest) error {
	runID, hostID := request.RunID, request.HostID
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTarget(request.RunID, request.HostID); err != nil {
		return err
	}
	if err := e.authority.Authorize(ctx, actor, store.ScopeRunsWrite); err != nil {
		return err
	}
	run, err := e.findRun(ctx, runID)
	if err != nil {
		return err
	}
	if err := run.mu.LockContext(ctx); err != nil {
		return err
	}
	defer run.mu.Unlock()
	if err := e.authority.Authorize(ctx, actor, store.ScopeRunsWrite); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	host, err := run.host(hostID)
	if err != nil {
		return err
	}
	host.cancel()
	if run.stopOnFailure {
		run.cancel()
	}
	return nil
}

func (e *Engine) Input(ctx context.Context, actor store.Actor, request InputRequest) error {
	runID, hostID, data := request.RunID, request.HostID, request.Data
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTarget(request.RunID, request.HostID); err != nil {
		return err
	}
	if len(data) == 0 || len(data) > 4096 {
		return ErrInput
	}
	if err := e.authority.Authorize(ctx, actor, store.ScopeRunsWrite); err != nil {
		return err
	}
	run, err := e.findRun(ctx, runID)
	if err != nil {
		return err
	}
	if err := run.mu.LockContext(ctx); err != nil {
		return err
	}
	host, err := run.host(hostID)
	var session remoteSession
	if err == nil {
		session = host.session
	}
	run.mu.Unlock()
	if err != nil {
		return err
	}
	if session == nil {
		return ErrInactive
	}
	if err := session.Input(ctx, data, func(admission context.Context) error {
		return e.authority.Authorize(admission, actor, store.ScopeRunsWrite)
	}); err != nil {
		if errors.Is(err, errInputTimeout) {
			e.failHost(host, "Terminal input timed out")
			return ErrInactive
		}
		return err
	}
	return nil
}

func (e *Engine) Pending(ctx context.Context, actor store.Actor, runID string) ([]HostKey, error) {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if err := e.authority.Authorize(ctx, actor, store.ScopeRunsRead); err != nil {
		return nil, err
	}
	keys := []HostKey{}
	run, err := e.findRun(ctx, runID)
	if errors.Is(err, ErrInactive) {
		return keys, nil
	}
	if err != nil {
		return nil, err
	}
	if err := run.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer run.mu.Unlock()
	if err := e.authority.Authorize(ctx, actor, store.ScopeRunsRead); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, host := range run.hosts {
		if host.pending != nil {
			keys = append(keys, *host.pending)
		}
	}
	return keys, nil
}

func (e *Engine) Approve(ctx context.Context, actor store.Actor, request ApprovalRequest) error {
	runID, hostID, fingerprint, accept := request.RunID, request.HostID, request.Fingerprint, request.Accept
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTarget(request.RunID, request.HostID); err != nil {
		return err
	}
	// Denial needs execution authority too; trust changes require host authority.
	if err := e.authority.Authorize(ctx, actor, store.ScopeRunsWrite); err != nil {
		return err
	}
	run, err := e.findRun(ctx, runID)
	if err != nil {
		return err
	}
	if err := run.mu.LockContext(ctx); err != nil {
		return err
	}
	defer run.mu.Unlock()
	if err := e.authority.Authorize(ctx, actor, store.ScopeRunsWrite); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	host, err := run.host(hostID)
	if err != nil {
		return err
	}
	if host.pending == nil || host.pending.Fingerprint != fingerprint {
		return ErrApproval
	}
	key := host.pending
	if host.phase != hostAwaitingApproval {
		return ErrApproval
	}
	if accept {
		if err := e.db.ApproveHostKey(ctx, actor, hostID, key.Address, key.Port, key.PublicKey); err != nil {
			return err
		}
	}
	host.pending = nil
	if accept {
		if err := host.transition(hostConnecting); err != nil {
			return err
		}
	}
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
	host.run.mu.Lock()
	if err := host.transition(hostAwaitingApproval); err != nil {
		host.run.mu.Unlock()
		return err
	}
	host.pending, host.decision = request, make(chan bool, 1)
	decision := host.decision
	host.run.mu.Unlock()
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
func (e *Engine) runHost(host *hostExecution, c store.Connection, script string, output io.Writer, ready func(), verify func(ssh.PublicKey) error) (*int, error) {
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
	session := newCommandSession(host.ctx)
	host.run.mu.Lock()
	host.session = session
	host.run.mu.Unlock()
	defer session.Cancel()
	session.start(e.runner, c, script, writer, ready, verify)
	result := <-session.Completion()
	code, runErr := result.code, result.err
	if errors.Is(runErr, errInputTimeout) {
		e.failHost(host, "Terminal input timed out")
	}
	if err := writer.Close(); err != nil && runErr == nil {
		runErr = err
	}
	return code, runErr
}
