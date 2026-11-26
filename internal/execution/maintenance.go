package execution

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"piped/internal/cron"
	"piped/internal/store"
)

func (e *Engine) reclaimServerOutput() {
	if !e.db.SupportsServerReclamation() || !e.mu.TryLock() {
		return
	}
	defer e.mu.Unlock()
	if len(e.active) != 0 || e.ctx.Err() != nil {
		return
	}
	e.maintenanceMu.Lock()
	if e.manualAdmissions != 0 {
		e.maintenanceMu.Unlock()
		return
	}
	maintenance, interrupt := context.WithCancel(e.ctx)
	e.maintenanceCancel = interrupt
	e.maintenanceMu.Unlock()
	defer func() {
		interrupt()
		e.maintenanceMu.Lock()
		e.maintenanceCancel = nil
		e.maintenanceMu.Unlock()
	}()
	check, cancel := context.WithTimeout(maintenance, 100*time.Millisecond)
	allowed, err := e.maintenanceWindow(check)
	cancel()
	if err != nil || !allowed {
		return
	}
	started := time.Now()
	err = e.db.ReclaimServerOutput(e.ctx, maintenance)
	if elapsed := time.Since(started); elapsed > e.maintenanceEstimate {
		e.maintenanceEstimate = elapsed
	}
	if err != nil && maintenance.Err() == nil {
		e.logger.Warn("database log reclamation deferred", "error", err)
	}
}

// Called under the admission lock. Keep a conservative three-second floor and
// defer for due-but-unprocessed schedules as well as upcoming cron occurrences.
func (e *Engine) maintenanceWindow(ctx context.Context) (bool, error) {
	// The watermark advances before schedule lookup/admission finishes. A due
	// batch still in progress must retain priority even with no active runs yet.
	if e.schedulePending != 0 {
		return false, nil
	}
	jobs, err := e.db.ScheduledJobs(ctx)
	if err != nil {
		return false, err
	}
	now := e.now()
	minute := now.Truncate(time.Minute)
	budget := max(e.maintenanceEstimate, store.ServerReclamationBudget)
	until := now.Add(2 * budget)
	for _, job := range jobs {
		schedule, err := cron.Parse(job.Cron)
		if err != nil {
			return false, err
		}
		if e.scheduleMinute < minute.Unix() && schedule.Matches(minute.In(time.Local)) {
			return false, nil
		}
		for next := minute.Add(time.Minute); next.Before(until); next = next.Add(time.Minute) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if schedule.Matches(next.In(time.Local)) {
				return false, nil
			}
		}
	}
	return true, nil
}

// maintenanceWorker owns retention, output compression and space reclamation.
// All goroutines join before execution ownership and database storage are closed.
type maintenanceWorker struct {
	db            *store.Store
	archiveReady  chan struct{}
	retentionDays int
	reclaim       func()
}

func (m *maintenanceWorker) start(ctx context.Context, logger *slog.Logger, reclaimer *store.SQLiteReclaimer, workers *sync.WaitGroup) {
	if reclaimer != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer reclaimer.Close()
			timer := time.NewTimer(0)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				more, err := reclaimer.Step(ctx)
				delay := time.Second
				if err != nil && ctx.Err() == nil {
					logger.Error("SQLite space reclamation failed", "error", err)
					delay = time.Minute
				} else if more {
					delay = 50 * time.Millisecond
				}
				timer.Reset(delay)
			}
		}()
	}
	workers.Add(2)
	go func() {
		defer workers.Done()
		retry := time.NewTicker(time.Minute)
		defer retry.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.archiveReady:
			case <-retry.C:
			}
			compression, cancel := context.WithTimeout(ctx, persistenceTimeout)
			err := m.db.CompressCompletedOutput(compression)
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error("execution log compression failed", "error", err)
			}
			m.reclaim()

		}
	}()
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			retention, cancel := context.WithTimeout(ctx, persistenceTimeout)
			err := m.db.PurgeRuns(retention, time.Now().AddDate(0, 0, -m.retentionDays).Unix())
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error("execution retention failed", "error", err)
			}
			// A single worker drains persisted completions. Coalesce wakeups rather
			// than allocating another goroutine or queued ID for every finished host.
			select {
			case m.archiveReady <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
