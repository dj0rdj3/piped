package store

import (
	"context"
	"errors"
	"time"

	"piped/internal/cron"
)

var ErrScheduleSkipped = errors.New("scheduled occurrence is no longer due or the job is already running")

type ScheduledJob struct {
	ID      string
	Version int64
	Cron    string
}

// ScheduledJobs is for the service's internal scheduler, never an HTTP identity.
// Execution is authorized when an administrator enables/edits the schedule.
func (s *Store) ScheduledJobs(ctx context.Context) ([]ScheduledJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,version,schedule_cron FROM jobs WHERE schedule_enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []ScheduledJob{}
	for rows.Next() {
		var job ScheduledJob
		if err := rows.Scan(&job.ID, &job.Version, &job.Cron); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// CreateScheduledRun has service authority, not cookie/bearer authority. The
// fresh minute, current configuration, duplicate slot, and overlap check are
// atomic with admission. The clock is injected solely for deterministic tests;
// production always supplies time.Now. Missed slots are never retried/queued.
func (s *Store) CreateScheduledRun(ctx context.Context, id, jobID string, version, minute int64, now func() time.Time) (CreatedRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreatedRun{}, err
	}
	defer tx.Rollback()
	if minute <= 0 || minute%60 != 0 || now().Truncate(time.Minute).Unix() != minute {
		return CreatedRun{}, ErrScheduleSkipped
	}
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs
		WHERE id = ? AND version = ? AND schedule_enabled = 1 AND schedule_after < ? AND last_schedule_minute < ?`,
		jobID, version, minute, minute))
	if errors.Is(err, ErrNotFound) {
		return CreatedRun{}, ErrScheduleSkipped
	}
	if err != nil {
		return CreatedRun{}, err
	}
	schedule, err := cron.Parse(job.Schedule.Cron)
	if err != nil {
		return CreatedRun{}, err
	}
	if !schedule.Matches(time.Unix(minute, 0).In(time.Local)) {
		return CreatedRun{}, ErrScheduleSkipped
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET last_schedule_minute = ? WHERE id = ?`, minute, jobID); err != nil {
		return CreatedRun{}, err
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE job_id = ? AND finished_at = 0)`, jobID).Scan(&active); err != nil {
		return CreatedRun{}, err
	}
	if active {
		if err := tx.Commit(); err != nil {
			return CreatedRun{}, err
		}
		return CreatedRun{}, ErrScheduleSkipped
	}
	run, connections, err := snapshotRun(ctx, tx, id, job, "schedule", minute)
	if err != nil {
		return CreatedRun{}, err
	}
	if now().Truncate(time.Minute).Unix() != minute {
		return CreatedRun{}, ErrScheduleSkipped
	}
	if err := tx.Commit(); err != nil {
		return CreatedRun{}, &UncertainCommit{RunID: run.ID, Cause: err}
	}
	return CreatedRun{Run: run, Job: job, Connections: connections}, nil
}
