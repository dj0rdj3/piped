package execution

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
	"piped/internal/store"
)

func TestReplayReadFailureCannotReplaceALiveController(t *testing.T) {
	f := newFixture(t, "parallel", false)
	started := make(chan struct{}, len(f.hosts))
	finish := make(chan struct{})
	var calls atomic.Int32
	f.engine.runner = func(ctx context.Context, _ store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-finish:
			code := 0
			return &code, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	input := StartRequest{JobID: f.job.ID, Version: f.job.Version, RequestID: "retry-during-read-failure"}
	run, err := f.engine.Start(context.Background(), f.actor, input)
	if err != nil {
		t.Fatal(err)
	}
	for range f.hosts {
		<-started
	}
	original, err := f.engine.findRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalStorage := f.engine.admissions
	f.engine.admissions = failingReplay{admissionStorage: originalStorage}
	if _, err := f.engine.Start(context.Background(), f.actor, input); err == nil {
		t.Fatal("fixture did not fail while reading replay host rows")
	}
	current, err := f.engine.findRun(context.Background(), run.ID)
	if err != nil || current != original {
		t.Fatal("a replay read error replaced a live controller with admission cleanup")
	}
	f.engine.admissions = originalStorage
	close(finish)
	if result := finished(t, f, run.ID); result.Status != "succeeded" || calls.Load() != int32(len(f.hosts)) {
		t.Fatal("replay read failure changed the running scripts or their durable outcome", result.Status, calls.Load())
	}
	if replay, err := f.engine.Start(context.Background(), f.actor, input); err != nil || replay.ID != run.ID || replay.Status != "succeeded" {
		t.Fatal("retry after read recovery lost the original outcome", err)
	}
}

type failingReplay struct{ admissionStorage }

func (failingReplay) AdmitRun(context.Context, store.Actor, store.RunAdmission) (store.Admission, error) {
	return nil, errors.New("host row stream failed after first row")
}
