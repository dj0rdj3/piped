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
}

type activeRun struct {
	hosts         map[string]*hostExecution
	cancel        context.CancelFunc
	stopOnFailure bool
}

type Engine struct {
	notifier         *notifications.Service
	db               *store.Store
	vault            *secrets.Vault
	logger           *slog.Logger
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	active           map[string]*activeRun
	wg               sync.WaitGroup
	now              func() time.Time
	schedulerStarted bool
	scheduleMinute   int64
	runner           func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error)
}

func New(ctx context.Context, db *store.Store, vault *secrets.Vault, logger *slog.Logger, retentionDays int) (*Engine, error) {
	if vault == nil || retentionDays < 1 || retentionDays > 36500 {
		return nil, ErrInput
	}
	if logger == nil {
		logger = slog.Default()
	}
	notifier, err := notifications.New(ctx, db, vault)
	if err != nil {
		return nil, err
	}
	if err := db.RecoverRuns(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	engine := &Engine{notifier: notifier, db: db, vault: vault, logger: logger, ctx: ctx, cancel: cancel, active: map[string]*activeRun{}, now: time.Now}
	engine.runner = engine.runSSH
	engine.wg.Add(1)
	go func() {
		defer engine.wg.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if err := db.PurgeRuns(ctx, time.Now().AddDate(0, 0, -retentionDays).Unix()); err != nil && ctx.Err() == nil {
				logger.Error("execution retention failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return engine, nil
}

// Done lets long-lived terminal connections follow service shutdown.
func (e *Engine) Done() <-chan struct{} { return e.ctx.Done() }

func (e *Engine) Close() {
	e.mu.Lock()
	e.cancel()
	e.mu.Unlock()
	e.wg.Wait()
}

func (e *Engine) Start(ctx context.Context, actor store.Actor, jobID string, version int64) (store.Run, error) {
	if version < 1 {
		return store.Run{}, ErrInput
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil {
		return store.Run{}, ErrInactive
	}
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	run, job, connections, err := e.db.CreateRun(ctx, actor, hex.EncodeToString(random), jobID, version)
	if err != nil {
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
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.execute(run.ID, job, connections, active) }()
	return run
}

func (e *Engine) execute(runID string, job store.Job, connections []store.Connection, active *activeRun) {
	defer active.cancel()
	var wg sync.WaitGroup
	// Each worker owns one result slot. Wait before reading the completed list;
	// result collection needs no shared flags or additional mutex.
	results := make([]string, len(connections))
	executeHost := func(index int, c store.Connection) {
		host := active.hosts[c.Host.ID]
		var code *int
		var err error
		status, message := "succeeded", ""
		if host.ctx.Err() == nil {
			if err = e.db.SetRunHost(context.Background(), runID, c.Host.ID, "connecting", "", nil); err == nil {
				code, err = e.runHost(host, c, job.Script, &outputWriter{engine: e, runID: runID, hostID: c.Host.ID, cancel: host.cancel}, func(input io.WriteCloser) {
					e.mu.Lock()
					host.input = input
					e.mu.Unlock()
					if updateErr := e.db.SetRunHost(context.Background(), runID, c.Host.ID, "running", "", nil); updateErr != nil {
						host.cancel()
						e.logger.Error("execution state failed", "error", updateErr)
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
		host.input, host.pending = nil, nil
		host.cancel()
		if (status == "failed" || status == "cancelled" || status == "interrupted") && job.StopOnFailure {
			active.cancel()
		}
		e.mu.Unlock()
		if err != nil && status == "failed" {
			if code != nil {
				e.logger.Warn("script exited", "run", runID, "host", c.Host.ID, "exit_code", *code)
			} else {
				e.logger.Warn("host execution failed", "run", runID, "host", c.Host.ID)
			}
		}
		updateErr := e.db.SetRunHost(context.Background(), runID, c.Host.ID, status, message, code)
		if updateErr != nil {
			e.logger.Error("execution state failed", "error", updateErr)
			status = "failed"
		}
		if updateErr == nil && len(c.Notifications) > 0 {
			e.wg.Add(1)
			go func() {
				defer e.wg.Done()
				e.notifier.NotifyHost(e.ctx, e.logger, runID, job.Name, c, status)
			}()
		}
		results[index] = status
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
	status := "succeeded"
	for _, outcome := range []string{"failed", "interrupted", "cancelled"} {
		if slices.Contains(results, outcome) {
			status = outcome
			break
		}
	}
	if err := e.db.FinishRun(context.Background(), runID, status); err != nil {
		e.logger.Error("execution completion failed", "error", err)
	}
	e.mu.Lock()
	delete(e.active, runID)
	e.mu.Unlock()
}

type outputWriter struct {
	engine        *Engine
	runID, hostID string
	cancel        context.CancelFunc
}

func (w *outputWriter) Write(data []byte) (int, error) {
	// Persist bounded transactions without waiting on a batching timer. The store
	// splits each transaction into small cursor chunks for history reads.
	written := 0
	for len(data) > 0 {
		size := min(len(data), store.MaxOutputBatchBytes)
		if err := w.engine.db.AppendOutput(context.Background(), w.runID, w.hostID, data[:size]); err != nil {
			if w.cancel != nil {
				w.cancel()
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
		host.cancel()
		return ErrInactive
	}
}

func (e *Engine) Pending(ctx context.Context, actor store.Actor, runID string) ([]HostKey, error) {
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
	trusted, err := e.db.TrustedHostKey(host.ctx, target.ID, target.Address, target.Port)
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
	if err := e.db.SetRunHost(host.ctx, runID, target.ID, "awaiting_approval", "SSH host key requires approval", nil); err != nil {
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
