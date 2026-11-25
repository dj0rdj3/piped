package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrJobName    = errors.New("job name already exists")
	ErrJobChanged = errors.New("job changed; refresh and try again")
	ErrJobHost    = errors.New("a selected host no longer exists; refresh and try again")
	ErrHostInUse  = errors.New("host is assigned to a job; remove it from the job first")
)

type JobSchedule struct {
	Cron    string `json:"cron"`
	Enabled bool   `json:"enabled"`
}

type Job struct {
	NotificationTargetIDs []string       `json:"notification_target_ids"`
	LatestRun             *JobRunSummary `json:"latest_run,omitempty"`
	Schedule              *JobSchedule   `json:"schedule"`
	SecretNames           []string       `json:"secret_names"`
	ID                    string         `json:"id"`
	Name                  string         `json:"name"`
	Script                string         `json:"script"`
	HostIDs               []string       `json:"host_ids"`
	ExecutionMode         string         `json:"execution_mode"`
	RunAsSudo             bool           `json:"run_as_sudo"`
	StopOnFailure         bool           `json:"stop_on_failure"`
	Version               int64          `json:"version"`
	CreatedAt             int64          `json:"created_at"`
}

type JobRunSummary struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StartedAt int64  `json:"started_at"`
}

// JobCopy identifies the version reviewed in the duplication form.
type JobCopy struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

const jobColumns = `id, name, script, execution_mode, stop_on_failure, version, created_at, run_as_sudo, schedule_cron, schedule_enabled`

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	job := Job{NotificationTargetIDs: []string{}, HostIDs: []string{}, SecretNames: []string{}, Schedule: &JobSchedule{}}
	err := row.Scan(&job.ID, &job.Name, &job.Script, &job.ExecutionMode, &job.StopOnFailure, &job.Version, &job.CreatedAt, &job.RunAsSudo, &job.Schedule.Cron, &job.Schedule.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return job, err
}

func (s *Store) Jobs(ctx context.Context, actor Actor) ([]Job, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeJobsRead)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs ORDER BY lower(name), name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	indices := map[string]int{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		indices[job.ID] = len(jobs)
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT job_id, host_id FROM job_hosts ORDER BY job_id, position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var jobID, hostID string
		if err := rows.Scan(&jobID, &hostID); err != nil {
			return nil, err
		}
		index := indices[jobID]
		jobs[index].HostIDs = append(jobs[index].HostIDs, hostID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT job_id,name FROM job_secrets ORDER BY job_id,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var jobID, name string
		if err := rows.Scan(&jobID, &name); err != nil {
			return nil, err
		}
		index := indices[jobID]
		jobs[index].SecretNames = append(jobs[index].SecretNames, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT job_id, target_id FROM job_notification_targets ORDER BY job_id, target_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var jobID, targetID string
		if err := rows.Scan(&jobID, &targetID); err != nil {
			rows.Close()
			return nil, err
		}
		jobs[indices[jobID]].NotificationTargetIDs = append(jobs[indices[jobID]].NotificationTargetIDs, targetID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := authorize(ctx, tx, actor, ScopeRunsRead); err == nil {
		rows, err := tx.QueryContext(ctx, `SELECT j.id, r.id, r.status, r.started_at
			FROM jobs j JOIN runs r ON r.run_sequence = (
				SELECT recent.run_sequence FROM runs recent WHERE recent.job_id = j.id
				ORDER BY recent.started_at DESC, recent.run_sequence DESC LIMIT 1
			)`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var jobID string
			var summary JobRunSummary
			if err := rows.Scan(&jobID, &summary.ID, &summary.Status, &summary.StartedAt); err != nil {
				return nil, err
			}
			jobs[indices[jobID]].LatestRun = &summary
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, ErrForbidden) {
		return nil, err
	}
	return jobs, tx.Commit()
}

// SaveJob atomically checks authorization, the current version, and all target
// references. Target order is preserved for future sequential execution.
func (s *Store) SaveJob(ctx context.Context, actor Actor, job Job, secretChanges []JobSecret, create bool) (Job, error) {
	return s.saveJob(ctx, actor, job, secretChanges, create, nil, nil)
}

// CopyJob copies secrets inside the creation transaction. Rebinding their
// authenticated ciphertext belongs to the service that owns the vault.
func (s *Store) CopyJob(ctx context.Context, actor Actor, job Job, secretChanges []JobSecret, source JobCopy, reencrypt func([]JobSecret) ([]JobSecret, error)) (Job, error) {
	return s.saveJob(ctx, actor, job, secretChanges, true, &source, reencrypt)
}

func (s *Store) saveJob(ctx context.Context, actor Actor, job Job, secretChanges []JobSecret, create bool, source *JobCopy, reencrypt func([]JobSecret) ([]JobSecret, error)) (Job, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeJobsWrite)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var copiedSecrets []JobSecret
	if source != nil {
		if err := authorize(ctx, tx, actor, ScopeJobsRead); err != nil {
			return Job{}, err
		}
		current, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, source.ID))
		if err != nil {
			return Job{}, err
		}
		if current.Version != source.Version {
			return Job{}, ErrJobChanged
		}
		values, err := jobSecretValues(ctx, tx, source.ID)
		if err != nil {
			return Job{}, err
		}
		copiedSecrets, err = reencrypt(values)
		if err != nil {
			return Job{}, err
		}
	}
	scheduleChanged := true
	if !create {
		current, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, job.ID))
		if err != nil {
			return Job{}, err
		}
		if current.Version != job.Version {
			return Job{}, ErrJobChanged
		}
		job.CreatedAt = current.CreatedAt
		if job.Schedule == nil {
			job.Schedule = current.Schedule
		}
		scheduleChanged = *job.Schedule != *current.Schedule
	}
	if job.Schedule == nil {
		job.Schedule = &JobSchedule{}
	}
	// Editing an enabled definition can change what will execute unattended.
	// Check execution authority inside the same transaction as the write.
	if job.Schedule.Enabled {
		if err := authorize(ctx, tx, actor, ScopeRunsWrite); err != nil {
			return Job{}, err
		}
	}
	if create {
		job.CreatedAt = time.Now().Unix()
		job.Version = 1
		_, err = tx.ExecContext(ctx, `INSERT INTO jobs (`+jobColumns+`, schedule_after) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			job.ID, job.Name, job.Script, job.ExecutionMode, job.StopOnFailure, job.Version, job.CreatedAt, job.RunAsSudo, job.Schedule.Cron, job.Schedule.Enabled, job.CreatedAt)
	} else {
		job.Version++
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET name = ?, script = ?, execution_mode = ?, stop_on_failure = ?, run_as_sudo = ?, schedule_cron = ?, schedule_enabled = ?, version = ?,
			schedule_after = CASE WHEN ? = 1 THEN ? ELSE schedule_after END WHERE id = ?`,
			job.Name, job.Script, job.ExecutionMode, job.StopOnFailure, job.RunAsSudo, job.Schedule.Cron, job.Schedule.Enabled, job.Version, scheduleChanged, time.Now().Unix(), job.ID)
	}
	if err != nil {
		if uniqueViolation(err) {
			return Job{}, ErrJobName
		}
		return Job{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_hosts WHERE job_id = ?`, job.ID); err != nil {
		return Job{}, err
	}
	for position, id := range job.HostIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_hosts (job_id, host_id, position) VALUES (?, ?, ?)`, job.ID, id, position); err != nil {
			if foreignKeyViolation(err) {
				return Job{}, ErrJobHost
			}
			return Job{}, err
		}
	}
	if err := saveJobNotifications(ctx, tx, &job, create, source); err != nil {
		return Job{}, err
	}
	if err := saveJobSecrets(ctx, tx, job.ID, secretChanges, copiedSecrets); err != nil {
		return Job{}, err
	}
	job.SecretNames, err = jobSecretNames(ctx, tx, job.ID)
	if err != nil {
		return Job{}, err
	}
	return job, tx.Commit()
}

func (s *Store) DeleteJob(ctx context.Context, actor Actor, id string, version int64) error {
	tx, err := s.beginAuthorized(ctx, actor, ScopeJobsWrite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM jobs WHERE id = ?`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != version {
		return ErrJobChanged
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
