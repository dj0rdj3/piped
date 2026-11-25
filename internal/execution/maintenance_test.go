package execution

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/store"
)

func TestMaintenanceWindowReservesTwiceEstimatedTime(t *testing.T) {
	f, clock, at := scheduledFixture(t, "* * * * *")
	f.engine.mu.Lock()
	defer f.engine.mu.Unlock()
	f.engine.scheduleMinute = at.Unix()
	f.engine.schedulePending = 1
	if allowed, err := f.engine.maintenanceWindow(context.Background()); err != nil || allowed {
		t.Fatal("maintenance admitted while due schedule lookup/admission was pending", err)
	}
	f.engine.schedulePending = 0
	for _, test := range []struct {
		second   int
		estimate time.Duration
		allowed  bool
	}{{50, 3 * time.Second, true}, {54, 3 * time.Second, true}, {55, 3 * time.Second, false}, {50, 6 * time.Second, false}, {40, 6 * time.Second, true}} {
		clock.Store(at.Add(time.Duration(test.second) * time.Second).Unix())
		f.engine.maintenanceEstimate = test.estimate
		allowed, err := f.engine.maintenanceWindow(context.Background())
		if err != nil || allowed != test.allowed {
			t.Fatalf("second=%d estimate=%s allowed=%v want=%v err=%v", test.second, test.estimate, allowed, test.allowed, err)
		}
	}
	f.engine.scheduleMinute = at.Add(-time.Minute).Unix()
	if allowed, err := f.engine.maintenanceWindow(context.Background()); err != nil || allowed {
		t.Fatal("maintenance admitted ahead of a due schedule", err)
	}
}

func TestManualStartInterruptsBeforeWaitingForMaintenance(t *testing.T) {
	f := newFixture(t, "sequential", false)
	f.engine.mu.Lock()
	f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
		code := 0
		return &code, nil
	}
	maintenance, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.engine.maintenanceMu.Lock()
	f.engine.maintenanceCancel = cancel
	f.engine.maintenanceMu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := f.engine.Start(context.Background(), f.actor, f.job.ID, f.job.Version)
		done <- err
	}()
	select {
	case <-maintenance.Done():
	case <-time.After(time.Second):
		f.engine.mu.Unlock()
		<-done
		t.Fatal("manual admission waited for maintenance before cancelling it")
	}
	select {
	case err := <-done:
		f.engine.mu.Unlock()
		t.Fatalf("admitted before maintenance released its lock: %v", err)
	default:
	}
	f.engine.maintenanceMu.Lock()
	waiters := f.engine.manualAdmissions
	f.engine.maintenanceCancel = nil
	f.engine.maintenanceMu.Unlock()
	f.engine.mu.Unlock()
	if waiters != 1 {
		t.Fatalf("waiting manual start did not prevent new maintenance: %d", waiters)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.engine.maintenanceMu.Lock()
	defer f.engine.maintenanceMu.Unlock()
	if f.engine.manualAdmissions != 0 {
		t.Fatal("manual admission priority was not released")
	}
}

func TestUnauthorizedManualStartDoesNotInterruptMaintenance(t *testing.T) {
	f := newFixture(t, "sequential", false)
	maintenance, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.engine.maintenanceMu.Lock()
	f.engine.maintenanceCancel = cancel
	f.engine.maintenanceMu.Unlock()
	actor := f.actor
	actor.SessionHash = "invalid-session"
	if _, err := f.engine.Start(context.Background(), actor, f.job.ID, f.job.Version); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("invalid session admitted", err)
	}
	if maintenance.Err() != nil {
		t.Fatal("unauthorized request interrupted maintenance")
	}
}

func TestCloseCancelsBeforeWaitingForMaintenanceAdmissionLock(t *testing.T) {
	f := newFixture(t, "sequential", false)
	f.engine.mu.Lock()
	done := make(chan struct{})
	go func() { f.engine.Close(); close(done) }()
	select {
	case <-f.engine.Done():
	case <-time.After(time.Second):
		f.engine.mu.Unlock()
		<-done
		t.Fatal("shutdown waited on maintenance before cancelling it")
	}
	f.engine.mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not join workers")
	}
}
