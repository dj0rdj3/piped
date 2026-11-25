package execution

import (
	"context"
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
