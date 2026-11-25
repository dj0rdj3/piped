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
	ErrJobTargets = errors.New("job targets must match between 1 and 1000 hosts")
)

const MaxJobHosts = 1000

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
	HostTags              []string       `json:"host_tags"`
	ExcludedHostIDs       []string       `json:"excluded_host_ids"`
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
	job := Job{NotificationTargetIDs: []string{}, HostIDs: []string{}, HostTags: []string{}, ExcludedHostIDs: []string{}, SecretNames: []string{}, Schedule: &JobSchedule{}}
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
	// Read ordered public relation values together, without loading ciphertext.
	rows, err = tx.QueryContext(ctx, `SELECT job_id, host_id AS value, 'host' AS kind, position AS sort_order FROM job_hosts
		UNION ALL SELECT job_id, tag, 'tag', 0 FROM job_host_tags
		UNION ALL SELECT job_id, host_id, 'exclusion', 0 FROM job_host_exclusions
		UNION ALL SELECT job_id, name, 'secret', 0 FROM job_secrets
		UNION ALL SELECT job_id, target_id, 'notification', 0 FROM job_notification_targets
		ORDER BY kind, job_id, sort_order, value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var jobID, value, kind string
		var position int
		if err := rows.Scan(&jobID, &value, &kind, &position); err != nil {
			return nil, err
		}
		job := &jobs[indices[jobID]]
		switch kind {
		case "host":
			job.HostIDs = append(job.HostIDs, value)
		case "tag":
			job.HostTags = append(job.HostTags, value)
		case "exclusion":
			job.ExcludedHostIDs = append(job.ExcludedHostIDs, value)
		case "secret":
			job.SecretNames = append(job.SecretNames, value)
		case "notification":
			job.NotificationTargetIDs = append(job.NotificationTargetIDs, value)
		}
	}
	if err := rows.Err(); err != nil {
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
	if job.HostTags == nil {
		job.HostTags = []string{}
	}
	if job.ExcludedHostIDs == nil {
		job.ExcludedHostIDs = []string{}
	}
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_host_tags WHERE job_id = ?`, job.ID); err != nil {
		return Job{}, err
	}
	for _, tag := range job.HostTags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_host_tags (job_id, tag) VALUES (?, ?)`, job.ID, tag); err != nil {
			return Job{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_host_exclusions WHERE job_id = ?`, job.ID); err != nil {
		return Job{}, err
	}
	for _, id := range job.ExcludedHostIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_host_exclusions (job_id, host_id) VALUES (?, ?)`, job.ID, id); err != nil {
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
