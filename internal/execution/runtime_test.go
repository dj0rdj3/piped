package execution

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/store"
)

type failingResults struct {
	resultStorage
	fail atomic.Bool
}

func (f *failingResults) FinishRun(ctx context.Context, id, status string) error {
	if f.fail.Load() {
		return errors.New("private storage failure must not be public")
	}
	return f.resultStorage.FinishRun(ctx, id, status)
}

func TestReadinessTracksDeferredResultsAndShutdown(t *testing.T) {
	f := newFixture(t, "parallel", false)
	results := &failingResults{resultStorage: f.db}
	results.fail.Store(true)
	f.engine.results = results
	f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
		code := 0
		return &code, nil
	}
	if !f.engine.Ready() {
		t.Fatal("healthy runtime is not ready")
	}
	run := startFixture(t, f)
	await(t, func() bool { return f.engine.pendingPersistence.Load() == 1 })
	status, err := f.engine.Runtime(context.Background(), f.actor)
	if err != nil || status.Ready || status.ActiveScripts != 0 || status.ActiveRuns != 1 || status.PendingResults != 1 || status.RetryingPersistence != 1 {
		t.Fatal("deferred durable result was not visible", status, err)
	}
	results.fail.Store(false)
	finished(t, f, run.ID)
	await(t, func() bool { return f.engine.Ready() })
	f.engine.Close()
	if f.engine.Ready() || f.engine.runtimeStatus().Shutdown != "stopped" {
		t.Fatal("closed runtime reported ready")
	}
}

func TestReadinessAndAdmissionStopOnOwnershipLoss(t *testing.T) {
	f := newFixture(t, "parallel", false)
	f.engine.loseOwnership()
	if f.engine.Ready() {
		t.Fatal("lost owner reported ready")
	}
	if _, err := f.engine.Start(context.Background(), f.actor, StartRequest{JobID: f.job.ID, Version: f.job.Version, RequestID: "lost-owner"}); !errors.Is(err, ErrInactive) {
		t.Fatal("lost owner admitted work", err)
	}
	f.engine.Close()
}

func TestMissingAdmissionIdentityCannotReachStorageOrSSH(t *testing.T) {
	f := newFixture(t, "parallel", false)
	_, err := f.engine.Start(context.Background(), f.actor, StartRequest{JobID: f.job.ID, Version: f.job.Version})
	if !errors.Is(err, store.ErrAdmissionKey) {
		t.Fatal("manual identity was optional", err)
	}
	runs, err := f.db.Runs(context.Background(), f.actor, f.job.ID)
	if err != nil || len(runs) != 0 {
		t.Fatal("invalid command created history", err)
	}
}

func TestInvalidControlTargetsShareValidation(t *testing.T) {
	f := newFixture(t, "parallel", false)
	ctx := context.Background()
	for _, id := range []string{"", "bad\x00id", "bad\xffid"} {
		for _, err := range []error{
			f.engine.Cancel(ctx, f.actor, HostRequest{RunID: id, HostID: "host"}),
			f.engine.Input(ctx, f.actor, InputRequest{RunID: "run", HostID: id, Data: "input"}),
			f.engine.Approve(ctx, f.actor, ApprovalRequest{RunID: id, HostID: "host", Fingerprint: "key"}),
		} {
			if !errors.Is(err, ErrInput) {
				t.Fatal("invalid command reached live lookup", err)
			}
		}
	}
}

func TestRuntimeDiagnosticsNeverWaitOnAControllerLock(t *testing.T) {
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
	controller.mu.Lock()
	snapshots := make(chan RuntimeStatus, 1)
	go func() { snapshots <- f.engine.runtimeStatus() }()
	var status RuntimeStatus
	select {
	case status = <-snapshots:
		controller.mu.Unlock()
	case <-time.After(time.Second):
		controller.mu.Unlock()
		<-snapshots
		t.Fatal("diagnostics waited for a controller lock")
	}
	if status.ActiveRuns != 1 || status.BusyRuns != 1 {
		t.Fatal("busy runtime snapshot was not explicit", status)
	}
}
