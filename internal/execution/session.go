package execution

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/lifecycle"
	"piped/internal/store"
)

// A session owns terminal input, cancellation and one remote completion. It
// cannot restart a command. Persistence has a separate lifetime and owner.
type remoteSession interface {
	Input(context.Context, string, func(context.Context) error) error
	Cancel()
	Completion() <-chan sessionResult
}

type sessionResult struct {
	code *int
	err  error
}
type sshRunner func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error)

type commandSession struct {
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	input        io.WriteCloser
	closed       bool
	inputGate    chan struct{}
	completed    chan sessionResult
	lastWrite    *terminalWrite
	inputFailure error
}

// Each admitted write has one immutable deadline and one published outcome.
// Completion and the input caller observe the same result under the session lock.
type terminalWrite struct {
	deadline    time.Time
	done        chan struct{}
	completedAt time.Time
	err         error
}

var errInputTimeout = errors.New("terminal input timed out")

func newCommandSession(parent context.Context) *commandSession {
	ctx, cancel := context.WithCancel(parent)
	return &commandSession{ctx: ctx, cancel: cancel, completed: make(chan sessionResult, 1), inputGate: make(chan struct{}, 1)}
}

func (s *commandSession) start(runner sshRunner, connection store.Connection, script string, output io.Writer, ready func(), verify func(ssh.PublicKey) error) {
	go func() {
		code, err := runner(s.ctx, connection, script, output, func(input io.WriteCloser) {
			s.mu.Lock()
			if !s.closed && s.ctx.Err() == nil {
				s.input = input
			}
			s.mu.Unlock()
			ready()
		}, verify)
		s.mu.Lock()
		s.closed = true
		s.input = nil
		write := s.lastWrite
		s.mu.Unlock()
		// A remote exit cannot overtake an admitted input and hide its timeout.
		// Join its outcome within the original write budget, independently of
		// whether the HTTP caller has resumed to report the failure yet.
		if write != nil {
			_ = s.waitInput(write)
		}
		s.mu.Lock()
		if s.inputFailure != nil {
			code, err = nil, s.inputFailure
		}
		s.mu.Unlock()
		s.completed <- sessionResult{code: code, err: err}
		close(s.completed)
	}()
}

func (s *commandSession) Cancel()                          { s.cancel() }
func (s *commandSession) Completion() <-chan sessionResult { return s.completed }

func (s *commandSession) Input(ctx context.Context, data string, admit func(context.Context) error) error {
	// Queueing follows the caller's budget. Once admitted, deliver exactly one
	// bounded write even if the browser disconnects; never retry terminal input.
	select {
	case s.inputGate <- struct{}{}:
		defer func() { <-s.inputGate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return ErrInactive
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Permission may expire or be revoked while queued. Recheck it after
	// acquiring the gate, before any bytes enter the remote terminal.
	if admit == nil {
		return ErrInput
	}
	if err := admit(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	input := s.input
	inactive := s.closed || s.ctx.Err() != nil
	if input == nil || inactive {
		s.mu.Unlock()
		return ErrInactive
	}
	write := &terminalWrite{deadline: time.Now().Add(lifecycle.InputTimeout), done: make(chan struct{})}
	s.lastWrite = write
	s.mu.Unlock()
	go func() {
		count, err := io.WriteString(input, data)
		if err == nil && count != len(data) {
			err = io.ErrShortWrite
		}
		s.mu.Lock()
		write.completedAt, write.err = time.Now(), err
		close(write.done)
		s.mu.Unlock()
	}()
	return s.waitInput(write)
}

func (s *commandSession) waitInput(write *terminalWrite) error {
	timer := time.NewTimer(time.Until(write.deadline))
	defer timer.Stop()
	select {
	case <-write.done:
	case <-timer.C:
	}
	s.mu.Lock()
	timedOut := write.completedAt.IsZero() || !write.completedAt.Before(write.deadline)
	err := write.err
	if timedOut {
		// Close admission before releasing the input gate. Engine failure handling
		// runs after this return and must not race another write into the session.
		s.closed = true
		s.input = nil
		s.inputFailure = errInputTimeout
	}
	s.mu.Unlock()
	if timedOut {
		return errInputTimeout
	}
	if err != nil && (s.ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe)) {
		return ErrInactive
	}
	return err
}
