package execution

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/auth"
	"piped/internal/store"
)

type pausedAuthority struct {
	commandAuthority
	ready  chan struct{}
	resume chan struct{}
	once   sync.Once
}

func (authority *pausedAuthority) Authorize(ctx context.Context, actor store.Actor, scope string) error {
	err := authority.commandAuthority.Authorize(ctx, actor, scope)
	if err == nil {
		authority.once.Do(func() { close(authority.ready); <-authority.resume })
	}
	return err
}

func controlFixture(t *testing.T) (*engineFixture, store.Run, *runController, *hostExecution) {
	t.Helper()
	f := newFixture(t, "parallel", false)
	f.engine.runner = func(ctx context.Context, _ store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	run := startFixture(t, f)
	controller, err := f.engine.findRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	host := controller.hosts[f.hosts[0].ID]
	await(t, func() bool { controller.mu.Lock(); defer controller.mu.Unlock(); return host.phase == hostConnecting })
	controller.mu.Lock()
	host.phase = hostAwaitingApproval
	host.pending = &HostKey{HostID: f.hosts[0].ID, Fingerprint: "reviewed-key"}
	host.decision = make(chan bool, 1)
	input := &heldInput{ready: make(chan struct{}), release: make(chan struct{})}
	input.Close()
	session := newCommandSession(host.ctx)
	session.input = input
	host.session = session
	t.Cleanup(session.Cancel)
	controller.mu.Unlock()
	return f, run, controller, host
}

func TestControlsRecheckAuthorityAfterWaiting(t *testing.T) {
	for _, operation := range []string{"cancel", "accept-key", "deny-key", "read-keys", "input"} {
		for _, invalidation := range []string{"caller-cancelled", "session-revoked", "password-changed"} {
			name := operation + "/" + invalidation
			t.Run(name, func(t *testing.T) {
				f, run, controller, host := controlFixture(t)
				pause := &pausedAuthority{commandAuthority: f.db, ready: make(chan struct{}), resume: make(chan struct{})}
				f.engine.authority = pause
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					switch operation {
					case "cancel":
						done <- f.engine.Cancel(ctx, f.actor, HostRequest{RunID: run.ID, HostID: f.hosts[0].ID})
					case "accept-key", "deny-key":
						done <- f.engine.Approve(ctx, f.actor, ApprovalRequest{RunID: run.ID, HostID: f.hosts[0].ID, Fingerprint: "reviewed-key", Accept: operation == "accept-key"})
					case "input":
						done <- f.engine.Input(ctx, f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "must not be written"})
					case "read-keys":
						_, err := f.engine.Pending(ctx, f.actor, run.ID)
						done <- err
					}
				}()
				<-pause.ready
				want := context.Canceled
				switch invalidation {
				case "session-revoked":
					if err := f.db.DeleteSession(context.Background(), f.actor.SessionHash); err != nil {
						close(pause.resume)
						t.Fatal(err)
					}
					want = store.ErrUnauthorized
				case "password-changed":
					if err := f.db.ChangePassword(context.Background(), f.actor, f.actor.ID, "replacement-fixture-hash"); err != nil {
						close(pause.resume)
						t.Fatal(err)
					}
					want = store.ErrUnauthorized
				default:
					cancel()
				}
				close(pause.resume)
				err := <-done
				if !errors.Is(err, want) {
					t.Errorf("stale control authority was accepted: got %v, want %v", err, want)
				}
				controller.mu.Lock()
				changed := host.ctx.Err() != nil || host.pending == nil
				controller.mu.Unlock()
				if changed {
					t.Fatal("a cancelled/revoked request changed live execution state")
				}
			})
		}
	}
}

func TestControlsDoNotTrustCachedScopesOrFallBackToBrowserAuthority(t *testing.T) {
	f, run, controller, host := controlFixture(t)
	ctx := context.Background()
	key, token, err := auth.NewAPIKey(f.actor.User, "Host definitions only", []string{store.ScopeHostsWrite}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.CreateLocalAPIKey(ctx, f.actor.User, key, auth.TokenHash(token)); err != nil {
		t.Fatal(err)
	}
	actor, err := f.db.ResolveAPIKey(ctx, auth.TokenHash(token), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	actor.Scopes = []string{store.ScopeRunsRead, store.ScopeRunsWrite, store.ScopeHostsWrite}
	actor.SessionHash = f.actor.SessionHash
	commands := []func() error{
		func() error { return f.engine.Cancel(ctx, actor, HostRequest{RunID: run.ID, HostID: f.hosts[0].ID}) },
		func() error {
			return f.engine.Input(ctx, actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "forbidden"})
		},
		func() error { _, err := f.engine.Pending(ctx, actor, run.ID); return err },
		func() error {
			return f.engine.Approve(ctx, actor, ApprovalRequest{RunID: run.ID, HostID: f.hosts[0].ID, Fingerprint: "reviewed-key", Accept: false})
		},
		func() error {
			return f.engine.Approve(ctx, actor, ApprovalRequest{RunID: run.ID, HostID: f.hosts[0].ID, Fingerprint: "reviewed-key", Accept: true})
		},
	}
	for _, command := range commands {
		if err := command(); !errors.Is(err, store.ErrForbidden) {
			t.Fatal("cached scopes or a browser session upgraded a bearer key", err)
		}
	}
	controller.mu.Lock()
	changed := host.ctx.Err() != nil || host.pending == nil
	session := host.session.(*commandSession)
	controller.mu.Unlock()
	if changed || session.input.(*heldInput).writes.Load() != 0 {
		t.Fatal("forged cached authority changed an execution or wrote input")
	}
}

func TestControlLockWaitHonorsCallerCancellation(t *testing.T) {
	f, run, controller, _ := controlFixture(t)
	pause := &pausedAuthority{commandAuthority: f.db, ready: make(chan struct{}), resume: make(chan struct{})}
	f.engine.authority = pause
	controller.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.engine.Cancel(ctx, f.actor, HostRequest{RunID: run.ID, HostID: f.hosts[0].ID}) }()
	<-pause.ready
	cancel()
	close(pause.resume)
	select {
	case err := <-done:
		controller.mu.Unlock()
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled lock wait did not fail", err)
		}
	case <-time.After(time.Second):
		controller.mu.Unlock()
		<-done
		t.Fatal("control request remained blocked after caller cancellation")
	}
}

type cancellingAuthority struct {
	commandAuthority
	cancel context.CancelFunc
	calls  int
}

func (authority *cancellingAuthority) Authorize(ctx context.Context, actor store.Actor, scope string) error {
	err := authority.commandAuthority.Authorize(ctx, actor, scope)
	authority.calls++
	if err == nil && authority.calls == 2 {
		authority.cancel()
	}
	return err
}

func TestControlsCancelledDuringFinalAuthorizationDoNotChangeState(t *testing.T) {
	for _, operation := range []string{"cancel", "accept-key", "deny-key", "read-keys", "input"} {
		t.Run(operation, func(t *testing.T) {
			f, run, controller, host := controlFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.engine.authority = &cancellingAuthority{commandAuthority: f.db, cancel: cancel}
			var err error
			switch operation {
			case "cancel":
				err = f.engine.Cancel(ctx, f.actor, HostRequest{RunID: run.ID, HostID: f.hosts[0].ID})
			case "accept-key", "deny-key":
				err = f.engine.Approve(ctx, f.actor, ApprovalRequest{RunID: run.ID, HostID: f.hosts[0].ID, Fingerprint: "reviewed-key", Accept: operation == "accept-key"})
			case "input":
				err = f.engine.Input(ctx, f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "must not be written"})
			case "read-keys":
				_, err = f.engine.Pending(ctx, f.actor, run.ID)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled control was admitted: %v", err)
			}
			controller.mu.Lock()
			changed := host.ctx.Err() != nil || host.pending == nil
			controller.mu.Unlock()
			if changed {
				t.Fatal("cancelled control changed execution state")
			}
		})
	}
}

func TestQueuedControlsRejectRevokedPersonalKeys(t *testing.T) {
	for _, operation := range []string{"cancel", "accept-key", "deny-key", "read-keys", "input"} {
		t.Run(operation, func(t *testing.T) {
			f, run, controller, host := controlFixture(t)
			ctx := context.Background()
			key, token, err := auth.NewUserAPIKey(f.actor.User, "Execution controls", []string{store.ScopeRunsRead, store.ScopeRunsWrite, store.ScopeHostsWrite}, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.db.CreateUserAPIKey(ctx, f.actor, key, auth.TokenHash(token)); err != nil {
				t.Fatal(err)
			}
			actor, err := f.db.ResolveAPIKey(ctx, auth.TokenHash(token), time.Now().Unix())
			if err != nil {
				t.Fatal(err)
			}
			pause := &pausedAuthority{commandAuthority: f.db, ready: make(chan struct{}), resume: make(chan struct{})}
			f.engine.authority = pause
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "cancel":
					done <- f.engine.Cancel(ctx, actor, HostRequest{RunID: run.ID, HostID: f.hosts[0].ID})
				case "accept-key", "deny-key":
					done <- f.engine.Approve(ctx, actor, ApprovalRequest{RunID: run.ID, HostID: f.hosts[0].ID, Fingerprint: "reviewed-key", Accept: operation == "accept-key"})
				case "read-keys":
					_, err := f.engine.Pending(ctx, actor, run.ID)
					done <- err
				case "input":
					done <- f.engine.Input(ctx, actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "must not be written"})
				}
			}()
			<-pause.ready
			err = f.db.RevokeUserAPIKey(ctx, f.actor, key.ID)
			close(pause.resume)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, store.ErrUnauthorized) {
				t.Fatal("revoked personal key retained execution authority", err)
			}
			controller.mu.Lock()
			changed := host.ctx.Err() != nil || host.pending == nil
			session := host.session.(*commandSession)
			controller.mu.Unlock()
			if changed || session.input.(*heldInput).writes.Load() != 0 {
				t.Fatal("revoked personal key changed execution state or wrote input")
			}
		})
	}
}
