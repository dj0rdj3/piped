package execution

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
	"piped/internal/store"
)

func TestRepeatedStartIdentityNeverRelaunchesScripts(t *testing.T) {
	f := newFixture(t, "parallel", false)
	var calls atomic.Int32
	f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
		calls.Add(1)
		code := 0
		return &code, nil
	}
	input := StartRequest{JobID: f.job.ID, Version: f.job.Version, RequestID: "reviewed-retry"}
	var workers sync.WaitGroup
	ids := make(chan string, 6)
	for range 6 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			run, err := f.engine.Start(context.Background(), f.actor, input)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- run.ID
		}()
	}
	workers.Wait()
	close(ids)
	var id string
	for current := range ids {
		if id != "" && current != id {
			t.Fatal("duplicate start created another execution")
		}
		id = current
	}
	if id == "" {
		t.Fatal("no admission succeeded")
	}
	finished(t, f, id)
	f.engine.Close()
	restarted, err := New(context.Background(), f.db, f.vault, nil, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.runner = f.engine.runner
	run, err := restarted.Start(context.Background(), f.actor, input)
	if err != nil || run.ID != id || run.Status != "succeeded" || calls.Load() != int32(len(f.hosts)) {
		t.Fatal("retry after restart replayed scripts or lost the result", run.ID, err, calls.Load())
	}
}

type lostCommitAck struct{ admissionStorage }

func (s lostCommitAck) AdmitRun(ctx context.Context, actor store.Actor, input store.RunAdmission) (store.Admission, error) {
	admitted, err := s.admissionStorage.AdmitRun(ctx, actor, input)
	if err != nil {
		return nil, err
	}
	if created, ok := admitted.(store.CreatedRun); ok {
		return nil, &store.UncertainCommit{RunID: created.Run.ID, Cause: io.ErrUnexpectedEOF}
	}
	return admitted, nil
}

func TestExplicitUncertainCommitReconcilesWithoutStartingSSH(t *testing.T) {
	f := newFixture(t, "parallel", false)
	f.engine.admissions = lostCommitAck{admissionStorage: f.db}
	var calls atomic.Int32
	f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
		calls.Add(1)
		code := 0
		return &code, nil
	}
	input := StartRequest{JobID: f.job.ID, Version: f.job.Version, RequestID: "lost-ack"}
	_, err := f.engine.Start(context.Background(), f.actor, input)
	var uncertain *store.UncertainCommit
	if !errors.As(err, &uncertain) {
		t.Fatal("commit ambiguity was not explicit", err)
	}
	run := finished(t, f, uncertain.RunID)
	if run.Status != "failed" || calls.Load() != 0 {
		t.Fatal("uncertain commit launched a script", run.Status, calls.Load())
	}
	replay, err := f.engine.Start(context.Background(), f.actor, input)
	if err != nil || replay.ID != run.ID || replay.Status != "failed" || calls.Load() != 0 {
		t.Fatal("retry replayed an uncertain script", err)
	}
}
