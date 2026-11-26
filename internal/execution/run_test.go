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

func TestHostTerminalTransitionsCannotReviveExecution(t *testing.T) {
	for phase := hostQueued; phase <= hostRunning; phase++ {
		host := &hostExecution{phase: phase}
		if err := host.transition(hostStopped); err != nil {
			t.Fatal("a live host cannot stop", phase, err)
		}
		for next := hostQueued; next <= hostRunning; next++ {
			if err := host.transition(next); !errors.Is(err, ErrInactive) || host.phase != hostStopped {
				t.Fatal("stopped host resumed before result persistence", next, err)
			}
		}
		if err := host.transition(hostPersisted); err != nil {
			t.Fatal(err)
		}
		for next := hostQueued; next < hostPersisted; next++ {
			if err := host.transition(next); !errors.Is(err, ErrInactive) || host.phase != hostPersisted {
				t.Fatal("durable host returned to an earlier phase", next, err)
			}
		}
	}
}

func TestRunResultCombinationIsIndependentOfHostOrder(t *testing.T) {
	statuses := []string{"succeeded", "cancelled", "interrupted", "failed"}
	for left, a := range statuses {
		for right, b := range statuses {
			expected := statuses[max(left, right)]
			if combineStatus(a, b) != expected || combineStatus(b, a) != expected {
				t.Fatal("parallel host order changed outcome precedence", a, b)
			}
		}
	}
}

func TestControllerSeparatesStoppedScriptsFromDurableCompletion(t *testing.T) {
	f := newFixture(t, "parallel", false)
	restore := rejectPersistence(t, f, "run_hosts")
	var calls atomic.Int32
	f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
		calls.Add(1)
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
		if controller.phase != runPersisting {
			return false
		}
		for _, host := range controller.hosts {
			if host.phase != hostStopped || host.result.status != "succeeded" {
				return false
			}
		}
		return true
	})
	if current, err := f.db.Run(context.Background(), f.actor, run.ID); err != nil || current.FinishedAt != 0 {
		t.Fatal("unsaved results were announced as complete", current, err)
	}
	restore()
	finished(t, f, run.ID)
	await(t, func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		return controller.phase == runPersisted
	})
	if calls.Load() != int32(len(f.hosts)) {
		t.Fatal("result retries repeated scripts", calls.Load())
	}
}

func TestBlockedRunControlDoesNotLockAnotherRun(t *testing.T) {
	f := newFixture(t, "parallel", false)
	f.engine.runner = func(ctx context.Context, _ store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	first, second := startFixture(t, f), startFixture(t, f)
	controller, err := f.engine.findRun(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	blocked := make(chan struct{})
	defer func() { controller.mu.Unlock(); <-blocked }()
	go func() {
		_, _ = f.engine.Pending(context.Background(), f.actor, first.ID)
		close(blocked)
	}()
	completed := make(chan error, 1)
	go func() {
		_, err := f.engine.Pending(context.Background(), f.actor, second.ID)
		completed <- err
	}()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("one run's control lock blocked a different run")
	}
	// The deferred unlock lets the blocked call and normal shutdown drain.
}
