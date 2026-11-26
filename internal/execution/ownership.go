package execution

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"piped/internal/lifecycle"
	"piped/internal/store"
)

type runtimeOwner struct {
	owner              *store.ExecutionOwner
	ownershipLost      atomic.Bool
	shutdown           atomic.Uint32
	pendingPersistence atomic.Int64
	closeOnce          sync.Once
	persistenceCtx     context.Context
	stopPersistence    context.CancelFunc
	scripts            sync.WaitGroup
	workersStopped     chan struct{}
	drainStopped       chan struct{}
	ctx                context.Context
	cancel             context.CancelFunc
	logger             *slog.Logger
}

func (o *runtimeOwner) startMonitor(workers *sync.WaitGroup) {
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(lifecycle.OwnershipHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-o.ctx.Done():
				return
			case <-o.owner.Lost():
				o.loseOwnership()
				return
			case <-ticker.C:
				check, stop := context.WithTimeout(o.ctx, lifecycle.StorageTimeout)
				err := o.owner.Check(check)
				stop()
				if err != nil {
					if o.ctx.Err() == nil {
						o.logger.Error("execution ownership lost; stopping server")
						o.loseOwnership()
					}
					o.cancel()
					return
				}
			}
		}
	}()
}

func (o *runtimeOwner) release(pending bool) {
	if pending || o.ownershipLost.Load() {
		// Failed persistence has already exhausted its drain. Do not add
		// more database round trips while releasing an unhealthy session.
		if err := o.owner.Discard(); err != nil {
			o.logger.Error("discard execution ownership failed")
		}
		return
	}
	cleanup, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	if err := o.owner.MarkStopped(cleanup); err != nil && !o.ownershipLost.Load() {
		o.logger.Error("clear execution ownership generation failed")
	}
	cancel()
	if err := o.owner.Close(); err != nil {
		o.logger.Error("release execution ownership failed")
	}
}
