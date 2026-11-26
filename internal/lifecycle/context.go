// Package lifecycle defines the service's context and timeout boundaries.
package lifecycle

import (
	"context"
	"time"
)

const (
	RequestTimeout       = 15 * time.Second
	HTTPHeaderTimeout    = 5 * time.Second
	DialTimeout          = 5 * time.Second
	SSHHandshakeTimeout  = 15 * time.Second
	EnvironmentTimeout   = 5 * time.Second
	NotificationTimeout  = 10 * time.Second
	StartupTimeout       = 15 * time.Second
	StorageTimeout       = 5 * time.Second
	InputTimeout         = 5 * time.Second
	TerminalWriteTimeout = 10 * time.Second
	SSHCleanupTimeout    = 5 * time.Second
	HTTPShutdownTimeout  = 10 * time.Second
	OwnershipHeartbeat   = time.Second
	// A replacement fences the predecessor before waiting for detection and
	// remote cleanup. Include a heartbeat interval and scheduling margin.
	OwnershipGrace = OwnershipHeartbeat + StorageTimeout + SSHCleanupTimeout + time.Second
)

// Operation preserves the caller's values/deadline and also follows service
// cancellation. Its returned cancel releases the timer and service callback on every path.
// Script execution itself follows the run lifetime and has no request deadline.
func Operation(parent, service context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, budget)
	stop := context.AfterFunc(service, cancel)
	if service.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
	}
}
