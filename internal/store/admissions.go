package store

import (
	"context"
	"database/sql"
	"errors"

	"piped/internal/problem"
)

var ErrAdmissionKey = problem.New(problem.Invalid, "request_id must contain 1-128 ASCII letters, digits, dots, underscores, colons, or hyphens")
var ErrAdmissionConflict = problem.New(problem.Conflict, "request_id already identifies a different job or version")

// RunAdmission identifies the reviewed request separately from its server run ID.
// Request identities are scoped to the authenticated user and retained with runs.
type RunAdmission struct {
	ID, JobID, RequestID string
	Version              int64
}

// Admission is sealed: only a confirmed creation carries permission to launch.
// ExistingRun contains public history and cannot provide a private snapshot.
type Admission interface {
	admission()
	PublicRun() Run
}

type CreatedRun struct {
	Run         Run          `json:"run"`
	Job         Job          `json:"-"`
	Connections []Connection `json:"-"`
}

func (CreatedRun) admission()       {}
func (a CreatedRun) PublicRun() Run { return a.Run }

type ExistingRun struct {
	Run Run `json:"run"`
}

func (ExistingRun) admission()       {}
func (a ExistingRun) PublicRun() Run { return a.Run }

// UncertainCommit is produced only after attempting COMMIT on a new snapshot.
// It grants permission to reconcile the proposed ID, never to execute its script.
// The underlying failure is intentionally absent from the public message.
type UncertainCommit struct {
	RunID string
	Cause error
}

func (e *UncertainCommit) Error() string {
	return "execution admission outcome is uncertain; retry with the same request_id"
}
func (e *UncertainCommit) Unwrap() error { return e.Cause }

func ValidAdmissionKey(key string) bool {
	if len(key) == 0 || len(key) > 128 {
		return false
	}
	for _, character := range key {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' || character == ':') {
			return false
		}
	}
	return true
}

// AdmitRun reauthorizes before looking up a request identity. A replay returns
// public history only, never execution snapshots or permission to launch again.
// A COMMIT error returns only an UncertainCommit, never a launchable snapshot.
func (s *Store) AdmitRun(ctx context.Context, actor Actor, input RunAdmission) (Admission, error) {
	if !ValidAdmissionKey(input.RequestID) {
		return nil, ErrAdmissionKey
	}
	tx, err := s.beginAuthorized(ctx, actor, ScopeRunsWrite)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	{
		var runID, jobID string
		var version int64
		err := tx.QueryRowContext(ctx, `SELECT run_id, job_id, job_version FROM run_admissions WHERE user_id = ? AND request_id = ?`, actor.ID, input.RequestID).Scan(&runID, &jobID, &version)
		if err == nil {
			if jobID != input.JobID || version != input.Version {
				return nil, ErrAdmissionConflict
			}
			run, err := readRun(ctx, tx, runID)
			if err != nil {
				return nil, err
			}
			return ExistingRun{Run: run}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if !validLookupID(input.JobID) {
		return nil, ErrNotFound
	}
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, input.JobID))
	if err != nil {
		return nil, err
	}
	if job.Version != input.Version {
		return nil, ErrJobChanged
	}
	run, connections, err := snapshotRun(ctx, tx, input.ID, job, "manual", 0)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_admissions (user_id, request_id, run_id, job_id, job_version) VALUES (?, ?, ?, ?, ?)`,
		actor.ID, input.RequestID, run.ID, job.ID, job.Version); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, &UncertainCommit{RunID: run.ID, Cause: err}
	}
	return CreatedRun{Run: run, Job: job, Connections: connections}, nil
}

func (e *UncertainCommit) Kind() problem.Kind { return problem.Uncertain }
