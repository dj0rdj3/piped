package store

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"piped/internal/problem"
	"piped/internal/secrets"
)

const (
	ScopeRunsRead  = "runs:read"
	ScopeRunsWrite = "runs:write"
)

// Connection contains ciphertext for internal workers only. It is never serialized.
type Connection struct {
	Notifications                      []NotificationTarget
	JobID                              string
	JobSecrets                         []JobSecret
	Environment                        []secrets.Variable
	Host                               Host
	RunAsSudo                          bool
	Password, SudoPassword, Credential []byte
}

type Run struct {
	Trigger       string    `json:"trigger"`
	ScheduledAt   int64     `json:"scheduled_at"`
	ID            string    `json:"id"`
	JobID         string    `json:"job_id"`
	JobName       string    `json:"job_name"`
	ExecutionMode string    `json:"execution_mode"`
	RunAsSudo     bool      `json:"run_as_sudo"`
	StopOnFailure bool      `json:"stop_on_failure"`
	Status        string    `json:"status"`
	StartedAt     int64     `json:"started_at"`
	FinishedAt    int64     `json:"finished_at"`
	Hosts         []RunHost `json:"hosts"`
}

type RunHost struct {
	HostID     string `json:"host_id"`
	Name       string `json:"name"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	ExitCode   *int   `json:"exit_code"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
}

type OutputChunk struct {
	Offset int    `json:"offset,omitempty"`
	ID     int64  `json:"id"`
	Data   []byte `json:"data"`
}

// All execution sources share the same transactional snapshots and writes.
func snapshotRun(ctx context.Context, tx writeTransaction, id string, job Job, trigger string, scheduledAt int64) (Run, []Connection, error) {
	ids, err := jobHostIDs(ctx, tx, job.ID)
	if err != nil {
		return Run{}, nil, err
	}
	positions := make(map[string]int, len(ids))
	args := make([]any, len(ids))
	for index, hostID := range ids {
		positions[hostID] = index
		args[index] = hostID
	}
	rows, err := tx.QueryContext(ctx, `SELECT h.id, h.name, h.address, h.port, h.username, COALESCE(h.credential_id, ''), h.run_as_sudo, h.encrypted_password, h.encrypted_sudo_password, c.encrypted_secret
		FROM hosts h LEFT JOIN credentials c ON c.id = h.credential_id
		WHERE h.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return Run{}, nil, err
	}
	connections := make([]Connection, len(ids))
	count := 0
	for rows.Next() {
		var c Connection
		if err := rows.Scan(&c.Host.ID, &c.Host.Name, &c.Host.Address, &c.Host.Port, &c.Host.Username, &c.Host.CredentialID, &c.Host.RunAsSudo, &c.Password, &c.SudoPassword, &c.Credential); err != nil {
			rows.Close()
			return Run{}, nil, err
		}
		c.RunAsSudo = job.RunAsSudo && c.Host.RunAsSudo && c.Host.Username != "root"
		connections[positions[c.Host.ID]] = c
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Run{}, nil, err
	}
	if count != len(ids) {
		return Run{}, nil, ErrJobHost
	}
	environment, err := jobSecretValues(ctx, tx, job.ID)
	if err != nil {
		return Run{}, nil, err
	}
	notifications, err := notificationSnapshot(ctx, tx, job.ID)
	if err != nil {
		return Run{}, nil, err
	}
	for index := range connections {
		connections[index].Notifications = notifications
		connections[index].JobID = job.ID
		connections[index].JobSecrets = environment
	}
	run := Run{Trigger: trigger, ScheduledAt: scheduledAt, ID: id, JobID: job.ID, JobName: job.Name, ExecutionMode: job.ExecutionMode, RunAsSudo: job.RunAsSudo, StopOnFailure: job.StopOnFailure, Status: "running", StartedAt: time.Now().Unix(), Hosts: []RunHost{}}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs (id, job_id, job_name, script, execution_mode, stop_on_failure, status, started_at, run_as_sudo, run_source, scheduled_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, job.ID, job.Name, job.Script, job.ExecutionMode, job.StopOnFailure, run.Status, run.StartedAt, job.RunAsSudo, trigger, scheduledAt)
	if err != nil {
		return Run{}, nil, err
	}
	for position, c := range connections {
		h := RunHost{HostID: c.Host.ID, Name: c.Host.Name, Address: c.Host.Address, Port: c.Host.Port, Status: "queued"}
		_, err := tx.ExecContext(ctx, `INSERT INTO run_hosts (run_id, host_id, name, address, port, position, status) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, h.HostID, h.Name, h.Address, h.Port, position, h.Status)
		if err != nil {
			return Run{}, nil, err
		}
		run.Hosts = append(run.Hosts, h)
	}
	return run, connections, nil
}

func (s *Store) Runs(ctx context.Context, actor Actor, jobID string) ([]Run, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeRunsRead)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if !validLookupID(jobID) {
		return nil, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, job_id, job_name, execution_mode, stop_on_failure, status, started_at, finished_at, run_as_sudo, run_source, scheduled_at FROM runs WHERE job_id = ? ORDER BY started_at DESC, run_sequence DESC LIMIT 100`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		var run Run
		if err := rows.Scan(&run.ID, &run.JobID, &run.JobName, &run.ExecutionMode, &run.StopOnFailure, &run.Status, &run.StartedAt, &run.FinishedAt, &run.RunAsSudo, &run.Trigger, &run.ScheduledAt); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) Run(ctx context.Context, actor Actor, id string) (Run, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeRunsRead)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	return readRun(ctx, tx, id)
}

type runReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// The caller has already checked fresh read or admission authority. Reuse the
// current transaction for replay; never acquire another pooled connection here.
func readRun(ctx context.Context, reader runReader, id string) (Run, error) {
	if !validLookupID(id) {
		return Run{}, ErrNotFound
	}
	var run Run
	err := reader.QueryRowContext(ctx, `SELECT id, job_id, job_name, execution_mode, stop_on_failure, status, started_at, finished_at, run_as_sudo, run_source, scheduled_at FROM runs WHERE id = ?`, id).Scan(&run.ID, &run.JobID, &run.JobName, &run.ExecutionMode, &run.StopOnFailure, &run.Status, &run.StartedAt, &run.FinishedAt, &run.RunAsSudo, &run.Trigger, &run.ScheduledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	rows, err := reader.QueryContext(ctx, `SELECT host_id, name, address, port, status, message, exit_code, started_at, finished_at FROM run_hosts WHERE run_id = ? ORDER BY position`, id)
	if err != nil {
		return Run{}, err
	}
	hosts, err := readRunHosts(rows)
	if err != nil {
		return Run{}, err
	}
	run.Hosts = hosts
	return run, nil
}

type runHostRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

func readRunHosts(rows runHostRows) ([]RunHost, error) {
	defer rows.Close()
	hosts := []RunHost{}
	for rows.Next() {
		var host RunHost
		if err := rows.Scan(&host.HostID, &host.Name, &host.Address, &host.Port, &host.Status, &host.Message, &host.ExitCode, &host.StartedAt, &host.FinishedAt); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return hosts, nil
}

func (s *Store) SetRunHost(ctx context.Context, runID, hostID, status, message string, code *int) error {
	now := time.Now().Unix()
	finished := int64(0)
	if status == "succeeded" || status == "failed" || status == "cancelled" || status == "skipped" || status == "interrupted" {
		finished = now
	}
	_, err := s.db.ExecContext(ctx, `UPDATE run_hosts SET status = ?, message = ?, exit_code = ?, started_at = CASE WHEN started_at = 0 THEN ? ELSE started_at END, finished_at = ? WHERE run_id = ? AND host_id = ?`, status, message, code, now, finished, runID, hostID)
	if err == nil {
		s.notifyRunState(runID)
	}
	return err
}

func (s *Store) FinishRun(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET status = ?, finished_at = ? WHERE id = ?`, status, time.Now().Unix(), id)
	if err == nil {
		s.notifyRunState(id)
	}
	return err
}

const MaxOutputBatchBytes = 64 * 1024

func (s *Store) FailUnlaunchedRun(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE run_hosts SET status = 'failed',
		message = 'Run admission could not be confirmed; script was not started', finished_at = ?
		WHERE run_id = ? AND finished_at = 0`, now, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'failed', finished_at = ? WHERE id = ? AND finished_at = 0`, now, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notifyRunState(id)
	return nil
}

func (s *Store) AppendOutput(ctx context.Context, runID, hostID string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > MaxOutputBatchBytes {
		return errors.New("output batch exceeds 64 KiB")
	}
	// Keep cursor chunks small, but pay for one durable commit per SSH write
	// instead of a filesystem sync for every 4 KiB. Notify only committed data.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var finished int64
	err = tx.QueryRowContext(ctx, `SELECT finished_at FROM run_hosts WHERE run_id = ? AND host_id = ?`, runID, hostID).Scan(&finished)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if finished != 0 {
		return errors.New("cannot append output to a finished host")
	}
	for len(data) > 0 {
		size := min(len(data), 4096)
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_output(run_id, host_id, data) VALUES (?, ?, ?)`, runID, hostID, data[:size]); err != nil {
			return err
		}
		data = data[size:]
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notifyRun(runID)
	return nil
}

func (s *Store) RunOutput(ctx context.Context, actor Actor, runID, hostID string, after int64) ([]OutputChunk, error) {
	tx, err := s.beginRunOutput(ctx, actor, runID, hostID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	reader := outputReader{source: tx, ctx: ctx, runID: runID, hostID: hostID, cursor: after}
	output := []OutputChunk{}
	for len(output) < 16 {
		chunk, err := reader.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		output = append(output, chunk)
	}
	return output, nil
}

func (s *Store) beginRunOutput(ctx context.Context, actor Actor, runID, hostID string) (*authorizedTransaction, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeRunsRead)
	if err != nil {
		return nil, err
	}
	if !validLookupID(runID) || !validLookupID(hostID) {
		tx.Rollback()
		return nil, ErrNotFound
	}
	var found int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM run_hosts WHERE run_id = ? AND host_id = ?`, runID, hostID).Scan(&found)
	if err != nil {
		tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return tx, nil
}

// Application IDs cannot contain NUL or malformed UTF-8. Reject these before
// binding SQL text, including on engines that would otherwise accept the bytes.
func validLookupID(id string) bool {
	return utf8.ValidString(id) && !strings.ContainsRune(id, 0)
}

// RunOutputTail seeks backwards through the cursor index, reading only the
// latest visible bytes. Full output remains available through RunOutput cursors.
func (s *Store) RunOutputTail(ctx context.Context, actor Actor, runID, hostID string) ([]OutputChunk, bool, error) {
	tx, err := s.beginRunOutput(ctx, actor, runID, hostID)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	reader := outputReader{source: tx, ctx: ctx, runID: runID, hostID: hostID, descending: true}
	return outputPage(&reader, 0, 0)
}

var ErrOutputCursor = problem.New(problem.Invalid, "invalid history cursor")

// RunOutputBefore returns the page directly before a byte cursor. The offset
// preserves prefixes of the boundary chunk when a page starts mid-chunk.
func (s *Store) RunOutputBefore(ctx context.Context, actor Actor, runID, hostID string, before int64, offset int) ([]OutputChunk, bool, error) {
	if before < 1 || offset < 0 || offset > 4096 {
		return nil, false, ErrOutputCursor
	}
	tx, err := s.beginRunOutput(ctx, actor, runID, hostID)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	reader := outputReader{source: tx, ctx: ctx, runID: runID, hostID: hostID, cursor: before, descending: true, inclusive: offset > 0}
	return outputPage(&reader, before, offset)
}

// Both initial tails and older pages have the same byte bound and return
// ascending chunks. IDs and offsets refer to the immutable persisted bytes.
func outputPage(reader *outputReader, prefixID int64, prefixBytes int) ([]OutputChunk, bool, error) {
	chunks := []OutputChunk{}
	remaining := 256 * 1024
	more := false
	for {
		if remaining == 0 {
			if !more {
				var err error
				more, err = reader.hasMore()
				if err != nil {
					return nil, false, err
				}
			}
			break
		}
		chunk, err := reader.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		if chunk.ID == prefixID {
			if prefixBytes > len(chunk.Data) {
				return nil, false, ErrOutputCursor
			}
			chunk.Data = chunk.Data[:prefixBytes]
		}
		if len(chunk.Data) > remaining {
			chunk.Offset = len(chunk.Data) - remaining
			chunk.Data = chunk.Data[chunk.Offset:]
			more = true
		}
		remaining -= len(chunk.Data)
		chunks = append(chunks, chunk)
	}
	slices.Reverse(chunks)
	return chunks, more, nil
}

func (s *Store) TrustedHostKey(ctx context.Context, hostID, address string, port int) (string, error) {
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT public_key FROM ssh_host_keys WHERE host_id = ? AND address = ? AND port = ?`, hostID, address, port).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return key, err
}

func (s *Store) ApproveHostKey(ctx context.Context, actor Actor, hostID, address string, port int, key string) error {
	tx, err := s.beginAuthorized(ctx, actor, ScopeHostsWrite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := authorize(ctx, tx, actor, ScopeRunsWrite); err != nil {
		return err
	}
	// Delete/insert under the transaction lock is portable and atomic.
	if _, err := tx.ExecContext(ctx, `DELETE FROM ssh_host_keys WHERE host_id = ? AND address = ? AND port = ?`, hostID, address, port); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ssh_host_keys(host_id, address, port, public_key) VALUES (?, ?, ?, ?)`, hostID, address, port, key)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecoverRuns records interruption; scripts are never replayed after restart.
func (s *Store) RecoverRuns(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE run_hosts SET status = 'failed', message = 'Service stopped during execution', finished_at = ? WHERE finished_at = 0`, now); err != nil {
		return err
	}
	// A crash may occur after all host results commit but before the parent
	// completion does. Preserve their actual outcome rather than losing it.
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = CASE
		WHEN NOT EXISTS (SELECT 1 FROM run_hosts h WHERE h.run_id = runs.id) THEN 'failed'
		WHEN EXISTS (SELECT 1 FROM run_hosts h WHERE h.run_id = runs.id AND h.status = 'failed') THEN 'failed'
		WHEN EXISTS (SELECT 1 FROM run_hosts h WHERE h.run_id = runs.id AND h.status = 'interrupted') THEN 'interrupted'
		WHEN EXISTS (SELECT 1 FROM run_hosts h WHERE h.run_id = runs.id AND h.status = 'cancelled') THEN 'cancelled'
		ELSE 'succeeded' END, finished_at = ? WHERE finished_at = 0`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PurgeRuns(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM runs WHERE finished_at > 0 AND finished_at < ?`, before)
	return err
}

// DeleteCompletedRun supports development fixture cleanup. Active runs cannot
// be removed while workers are persisting output.
func (s *Store) DeleteCompletedRun(ctx context.Context, actor Actor, id string) error {
	tx, err := s.beginAuthorized(ctx, actor, ScopeRunsWrite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var finished int64
	err = tx.QueryRowContext(ctx, `SELECT finished_at FROM runs WHERE id = ?`, id).Scan(&finished)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if finished == 0 {
		return ErrRunActive
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

var ErrRunActive = problem.New(problem.Conflict, "active executions cannot be removed")
