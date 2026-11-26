package execution

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"piped/internal/cron"
	"piped/internal/store"
)

// StartScheduler begins at the next minute after startup. There is no startup
// scan or replay. Close cancels the timer and all runs through the engine context.
func (e *Engine) StartScheduler() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.schedulerStarted || e.ctx.Err() != nil {
		return
	}
	e.schedulerStarted = true
	e.scheduleMinute = e.now().Truncate(time.Minute).Unix()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for {
			now := e.now()
			next := now.Truncate(time.Minute).Add(time.Minute)
			timer := time.NewTimer(next.Sub(now))
			select {
			case <-e.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				e.runSchedules(e.now())
			}
		}
	}()
}

// Only the currently observed minute is considered, even after a delayed timer,
// suspension, or clock jump. The watermark also prevents same-minute rescans.
func (e *Engine) runSchedules(at time.Time) {
	minute := at.Truncate(time.Minute).Unix()
	e.mu.Lock()
	if e.ctx.Err() != nil || minute <= e.scheduleMinute {
		e.mu.Unlock()
		return
	}
	e.scheduleMinute = minute
	e.schedulePending++
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.schedulePending--
		e.mu.Unlock()
	}()
	lookup, cancel := e.operationContext(e.ctx)
	jobs, err := e.db.ScheduledJobs(lookup)
	cancel()
	if err != nil {
		if e.ctx.Err() == nil {
			e.logger.Error("schedule lookup failed", "error", err)
		}
		return
	}
	for _, candidate := range jobs {
		schedule, err := cron.Parse(candidate.Cron)
		if err != nil {
			e.logger.Error("stored cron schedule is invalid", "job", candidate.ID)
			continue
		}
		if !schedule.Matches(at.In(time.Local)) {
			continue
		}
		e.mu.Lock()
		if e.ctx.Err() != nil {
			e.mu.Unlock()
			return
		}
		random := make([]byte, 16)
		_, _ = rand.Read(random)
		admission, cancel := e.operationContext(e.ctx)
		run, job, connections, err := e.db.CreateScheduledRun(admission, hex.EncodeToString(random), candidate.ID, candidate.Version, minute, e.now)
		cancel()
		if err == nil {
			e.launch(run, job, connections)
		} else {
			e.observeOwnership(err)
			if run.ID != "" && !e.ownershipLost.Load() {
				e.reconcileAdmission(run.ID)
			}
		}
		e.mu.Unlock()
		if err != nil && !errors.Is(err, store.ErrScheduleSkipped) && e.ctx.Err() == nil {
			e.logger.Error("scheduled execution admission failed", "job", candidate.ID, "error", err)
		}
	}
}
