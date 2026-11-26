package execution

import (
	"context"
	"sync"
)

// controlLock is a zero-value-safe mutex with cancellable acquisition for API
// commands. Internal state transitions still join using Lock; diagnostics use
// TryLock. No goroutine is left behind when a caller abandons a lock wait.
type controlLock struct {
	once sync.Once
	held chan struct{}
}

func (lock *controlLock) initialize() { lock.once.Do(func() { lock.held = make(chan struct{}, 1) }) }
func (lock *controlLock) Lock()       { lock.initialize(); lock.held <- struct{}{} }
func (lock *controlLock) Unlock()     { <-lock.held }
func (lock *controlLock) TryLock() bool {
	lock.initialize()
	select {
	case lock.held <- struct{}{}:
		return true
	default:
		return false
	}
}
func (lock *controlLock) LockContext(ctx context.Context) error {
	lock.initialize()
	select {
	case lock.held <- struct{}{}:
		if err := ctx.Err(); err != nil {
			lock.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
