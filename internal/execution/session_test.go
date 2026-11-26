package execution

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/lifecycle"
	"piped/internal/store"
)

type heldInput struct {
	ready   chan struct{}
	release chan struct{}
	writes  atomic.Int32
	once    sync.Once
}

func (input *heldInput) Write(data []byte) (int, error) {
	if input.writes.Add(1) == 1 {
		close(input.ready)
	}
	<-input.release
	return len(data), nil
}
func (input *heldInput) Close() error { input.once.Do(func() { close(input.release) }); return nil }

func TestCommandSessionQueuedInputHonorsCancellationWithoutWriting(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "deadline"}[deadline], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				session := newCommandSession(context.Background())
				defer session.Cancel()
				input := &heldInput{ready: make(chan struct{}), release: make(chan struct{})}
				defer input.Close()
				session.input = input
				first := make(chan error, 1)
				go func() { first <- session.Input(context.Background(), "first", allowInput) }()
				<-input.ready
				var ctx context.Context
				var cancel context.CancelFunc
				want := context.Canceled
				if deadline {
					ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
					want = context.DeadlineExceeded
				} else {
					ctx, cancel = context.WithCancel(context.Background())
				}
				defer cancel()
				queued := make(chan error, 1)
				go func() { queued <- session.Input(ctx, "must never be delivered", allowInput) }()
				synctest.Wait() // Both writes are now durably blocked on the held input/gate.
				if deadline {
					time.Sleep(10 * time.Millisecond)
				} else {
					cancel()
				}
				synctest.Wait()
				select {
				case err := <-queued:
					if !errors.Is(err, want) {
						t.Fatal("queued input ignored caller cancellation", err)
					}
				case <-time.After(time.Second):
					input.Close()
					<-queued
					t.Fatal("queued input waited on a mutex beyond its cancellation")
				}
				input.Close()
				if err := <-first; err != nil {
					t.Fatal(err)
				}
				if input.writes.Load() != 1 {
					t.Fatal("cancelled queued input was delivered or retried", input.writes.Load())
				}
			})
		})
	}
}

func TestCommandSessionAdmittedInputSurvivesCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newCommandSession(context.Background())
		defer session.Cancel()
		input := &heldInput{ready: make(chan struct{}), release: make(chan struct{})}
		defer input.Close()
		session.input = input
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- session.Input(ctx, "one admitted write", allowInput) }()
		<-input.ready
		cancel()
		input.Close()
		if err := <-done; err != nil {
			t.Fatal("caller cancellation changed an admitted write", err)
		}
		if input.writes.Load() != 1 {
			t.Fatal("admitted input was retried", input.writes.Load())
		}
	})
}

func allowInput(context.Context) error { return nil }

func TestCommandSessionQueuedInputRechecksAuthorityBeforeWriting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newCommandSession(context.Background())
		defer session.Cancel()
		input := &heldInput{ready: make(chan struct{}), release: make(chan struct{})}
		defer input.Close()
		session.input = input
		first := make(chan error, 1)
		go func() { first <- session.Input(context.Background(), "first", allowInput) }()
		<-input.ready
		queued := make(chan error, 1)
		var revoked atomic.Bool
		go func() {
			queued <- session.Input(context.Background(), "revoked write", func(context.Context) error {
				if revoked.Load() {
					return store.ErrUnauthorized
				}
				return nil
			})
		}()
		synctest.Wait() // Prove revocation happens while this request is queued.
		revoked.Store(true)
		input.Close()
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if err := <-queued; !errors.Is(err, store.ErrUnauthorized) {
			t.Fatal("queued input skipped fresh authority", err)
		}
		if input.writes.Load() != 1 {
			t.Fatal("revoked queued input reached SSH", input.writes.Load())
		}
	})
}

func TestCommandSessionTimeoutClosesInputBeforeTheGateIsReleased(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newCommandSession(context.Background())
		defer session.Cancel()
		input := &heldInput{ready: make(chan struct{}), release: make(chan struct{})}
		defer input.Close()
		session.input = input
		done := make(chan error, 1)
		go func() { done <- session.Input(context.Background(), "stalled", allowInput) }()
		<-input.ready
		if err := <-done; !errors.Is(err, errInputTimeout) {
			t.Fatal("fixture did not time out", err)
		}
		// Engine failure handling happens after Input returns. No later caller may
		// enter the same writer during that scheduling window.
		if err := session.Input(context.Background(), "must not race the stalled write", allowInput); !errors.Is(err, ErrInactive) {
			t.Error("timed-out session accepted another write", err)
		}
		if input.writes.Load() != 1 {
			t.Fatal("a second writer entered after timeout", input.writes.Load())
		}
	})
}

func TestCommandSessionCancellationDuringAuthorizationDoesNotAdmitInput(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "deadline"}[deadline], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				session := newCommandSession(context.Background())
				defer session.Cancel()
				input := &heldInput{ready: make(chan struct{}), release: make(chan struct{})}
				input.Close()
				session.input = input
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				want := context.Canceled
				if deadline {
					want = context.DeadlineExceeded
				}
				err := session.Input(ctx, "must never be delivered", func(context.Context) error {
					if deadline {
						time.Sleep(time.Second)
						<-ctx.Done()
					} else {
						cancel()
					}
					return nil
				})
				if !errors.Is(err, want) || input.writes.Load() != 0 {
					t.Fatalf("cancelled admission wrote input: error=%v writes=%d", err, input.writes.Load())
				}
			})
		})
	}
}

type delayedInput struct {
	delay time.Duration
	done  chan struct{}
}

func (input delayedInput) Write(data []byte) (int, error) {
	defer close(input.done)
	time.Sleep(input.delay)
	return len(data), nil
}
func (delayedInput) Close() error { return nil }

func TestCommandSessionInputDeadlineBoundary(t *testing.T) {
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				session := newCommandSession(context.Background())
				defer session.Cancel()
				input := delayedInput{delay: lifecycle.InputTimeout + offset, done: make(chan struct{})}
				session.input = input
				defer func() { <-input.done; synctest.Wait() }()
				started := time.Now()
				err := session.Input(context.Background(), "one write", allowInput)
				if offset < 0 {
					if err != nil {
						t.Fatal("input completed before its deadline", err)
					}
				} else if !errors.Is(err, errInputTimeout) {
					t.Fatal("input at or after its deadline did not time out", err)
				}
				if time.Since(started) > lifecycle.InputTimeout {
					t.Fatal("input exceeded its admitted write budget")
				}
			})
		})
	}
}

func TestCommandSessionTimeoutRejectsAllQueuedWriters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newCommandSession(context.Background())
		defer session.Cancel()
		input := &heldInput{ready: make(chan struct{}), release: make(chan struct{})}
		defer input.Close()
		session.input = input
		first := make(chan error, 1)
		go func() { first <- session.Input(context.Background(), "stalled", allowInput) }()
		<-input.ready
		const callers = 32
		queued := make(chan error, callers)
		for range callers {
			go func() { queued <- session.Input(context.Background(), "must not race", allowInput) }()
		}
		synctest.Wait()
		if err := <-first; !errors.Is(err, errInputTimeout) {
			t.Fatal("stalled input did not time out", err)
		}
		for range callers {
			if err := <-queued; !errors.Is(err, ErrInactive) {
				t.Fatal("queued input survived the write timeout", err)
			}
		}
		if input.writes.Load() != 1 {
			t.Fatal("queued writers entered a timed-out terminal", input.writes.Load())
		}
	})
}

func TestCommandSessionCompletionRetainsAnInputTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newCommandSession(context.Background())
		defer session.Cancel()
		input := &heldInput{ready: make(chan struct{}), release: make(chan struct{})}
		defer input.Close()
		finish := make(chan struct{})
		session.start(func(_ context.Context, _ store.Connection, _ string, _ io.Writer, connected func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
			connected(input)
			<-finish
			input.Close()
			code := 0
			return &code, nil
		}, store.Connection{}, "", io.Discard, func() {}, nil)
		synctest.Wait()
		done := make(chan error, 1)
		go func() { done <- session.Input(context.Background(), "stalled", allowInput) }()
		<-input.ready
		if err := <-done; !errors.Is(err, errInputTimeout) {
			t.Fatal("fixture did not time out", err)
		}
		close(finish)
		result := <-session.Completion()
		if !errors.Is(result.err, errInputTimeout) || result.code != nil {
			t.Fatalf("remote success hid an input timeout: %+v", result)
		}
	})
}

func TestCommandSessionCompletionWaitsForAdmittedInput(t *testing.T) {
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				session := newCommandSession(context.Background())
				defer session.Cancel()
				input := delayedInput{delay: lifecycle.InputTimeout + offset, done: make(chan struct{})}
				defer func() { <-input.done; synctest.Wait() }()
				finish := make(chan struct{})
				session.start(func(_ context.Context, _ store.Connection, _ string, _ io.Writer, connected func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
					connected(input)
					<-finish
					code := 0
					return &code, nil
				}, store.Connection{}, "", io.Discard, func() {}, nil)
				synctest.Wait()
				done := make(chan error, 1)
				go func() { done <- session.Input(context.Background(), "one write", allowInput) }()
				synctest.Wait()
				// Remote completion wins the scheduling race with the input caller.
				close(finish)
				result := <-session.Completion()
				inputErr := <-done
				if offset < 0 {
					if result.err != nil || inputErr != nil || result.code == nil || *result.code != 0 {
						t.Fatalf("on-time input changed remote success: %+v, %v", result, inputErr)
					}
				} else if !errors.Is(result.err, errInputTimeout) || !errors.Is(inputErr, errInputTimeout) || result.code != nil {
					t.Fatalf("completion raced input timeout: %+v, %v", result, inputErr)
				}
			})
		})
	}
}
