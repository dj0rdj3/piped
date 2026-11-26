package execution

import (
	"context"
	"errors"
	"io"
	"testing"

	"golang.org/x/crypto/ssh"
	"piped/internal/store"
)

type closedInput struct{ err error }

func (input closedInput) Write([]byte) (int, error) { return 0, input.err }
func (closedInput) Close() error                    { return nil }

func TestClosedTerminalInputIsInactiveBeforeHostFinalization(t *testing.T) {
	for _, inputError := range []error{io.EOF, io.ErrClosedPipe} {
		t.Run(inputError.Error(), func(t *testing.T) {
			f := newFixture(t, "parallel", false)
			ready := make(chan struct{}, len(f.hosts))
			f.engine.runner = func(ctx context.Context, _ store.Connection, _ string, _ io.Writer, connected func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
				connected(closedInput{err: inputError})
				ready <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			}
			run := startFixture(t, f)
			for range f.hosts {
				<-ready
			}
			err := f.engine.Input(context.Background(), f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "input"})
			if !errors.Is(err, ErrInactive) {
				t.Fatal("a closed terminal was reported as a server failure", err)
			}
		})
	}
}

type pausedTimeoutSession struct {
	remoteSession
	ready  chan struct{}
	resume chan struct{}
}

func (session *pausedTimeoutSession) Input(ctx context.Context, data string, admit func(context.Context) error) error {
	err := session.remoteSession.Input(ctx, data, admit)
	if errors.Is(err, errInputTimeout) {
		close(session.ready)
		<-session.resume
	}
	return err
}

func TestInputTimeoutCannotBeLostToSuccessfulHostFinalization(t *testing.T) {
	f := newFixture(t, "parallel", false)
	input := &heldInput{ready: make(chan struct{}), release: make(chan struct{})}
	defer input.Close()
	finishRemote := make(chan struct{})
	defer close(finishRemote)
	f.engine.runner = func(ctx context.Context, connection store.Connection, _ string, _ io.Writer, connected func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		if connection.Host.ID == f.hosts[0].ID {
			connected(input)
			select {
			case <-finishRemote:
			case <-ctx.Done():
			}
			input.Close()
		}
		code := 0
		return &code, nil
	}
	run := startFixture(t, f)
	controller, err := f.engine.findRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		return controller.hosts[f.hosts[0].ID].phase == hostRunning
	})
	controller.mu.Lock()
	host := controller.hosts[f.hosts[0].ID]
	paused := &pausedTimeoutSession{remoteSession: host.session, ready: make(chan struct{}), resume: make(chan struct{})}
	host.session = paused
	controller.mu.Unlock()
	defer close(paused.resume)
	done := make(chan error, 1)
	go func() {
		done <- f.engine.Input(context.Background(), f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "stalled"})
	}()
	<-paused.ready
	// The caller has observed the timeout but cannot yet call Engine.failHost.
	finishRemote <- struct{}{}
	result := finished(t, f, run.ID)
	if result.Status != "failed" || result.Hosts[0].Status != "failed" || result.Hosts[0].Message != "Terminal input timed out" {
		t.Fatalf("remote completion won over the input failure: %+v", result)
	}
	// Joining is deferred until the paused caller has been allowed to return.
	t.Cleanup(func() {
		if err := <-done; !errors.Is(err, ErrInactive) {
			t.Error("timed-out input did not return inactive", err)
		}
	})
}

func TestTerminalInputTimeoutFailsHostAndRejectsQueuedInput(t *testing.T) {
	f := newFixture(t, "parallel", false)
	input := &heldInput{ready: make(chan struct{}), release: make(chan struct{})}
	defer input.Close()
	f.engine.runner = func(ctx context.Context, connection store.Connection, _ string, _ io.Writer, connected func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		if connection.Host.ID != f.hosts[0].ID {
			code := 0
			return &code, nil
		}
		connected(input)
		<-ctx.Done()
		input.Close()
		return nil, ctx.Err()
	}
	run := startFixture(t, f)
	controller, err := f.engine.findRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		return controller.hosts[f.hosts[0].ID].phase == hostRunning
	})
	request := InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "stalled"}
	first := make(chan error, 1)
	go func() { first <- f.engine.Input(context.Background(), f.actor, request) }()
	<-input.ready
	queued := make(chan error, 1)
	go func() { queued <- f.engine.Input(context.Background(), f.actor, request) }()
	if err := <-first; !errors.Is(err, ErrInactive) {
		t.Fatal("input timeout did not return an inactive execution", err)
	}
	if err := <-queued; !errors.Is(err, ErrInactive) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("queued input was accepted after timeout", err)
	}
	result := finished(t, f, run.ID)
	if result.Status != "failed" || result.Hosts[0].Status != "failed" || result.Hosts[0].Message != "Terminal input timed out" {
		t.Fatalf("input timeout was not durably recorded as a failure: %+v", result)
	}
	if input.writes.Load() != 1 {
		t.Fatal("timed-out input was retried or overlapped", input.writes.Load())
	}
}
