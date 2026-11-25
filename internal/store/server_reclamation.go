package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strconv"
	"time"
)

const ServerReclamationBudget = 3 * time.Second

func (s *Store) SupportsServerReclamation() bool {
	return s.db.dialect == "postgres" || s.db.dialect == "mariadb"
}

// ReclaimServerOutput is called only while this engine has no active jobs.
// Recheck persisted runs for other admissions, use fail-fast metadata locks,
// and never fall back to a blocking table-copy algorithm or VACUUM FULL.
// interrupt requests server-confirmed cancellation; ctx bounds the worker's life.
func (s *Store) ReclaimServerOutput(ctx, interrupt context.Context) error {
	if !s.SupportsServerReclamation() {
		return nil
	}
	if err := interrupt.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, ServerReclamationBudget)
	defer cancel()
	acquire, release := context.WithTimeout(ctx, 20*time.Millisecond)
	conn, err := s.db.DB.Conn(acquire)
	release()
	if err != nil {
		return err
	}
	defer conn.Close()
	// Discard the session so maintenance timeouts never leak into job queries.
	defer conn.Raw(func(any) error { return driver.ErrBadConn })
	stop, err := s.interruptServerReclamation(ctx, interrupt, conn)
	if err != nil {
		return err
	}
	// Join cancellation before discarding the session: its ID must never target
	// a pooled connection that has already been handed to a running job.
	defer stop()
	settings := []string{`SET lock_timeout = '1ms'`, `SET statement_timeout = '1s'`}
	if s.db.dialect == "mariadb" {
		settings = []string{`SET SESSION lock_wait_timeout = 0, innodb_lock_wait_timeout = 1, max_statement_time = 1`}
	}
	for _, setting := range settings {
		if err := interrupt.Err(); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, setting); err != nil {
			return err
		}
	}
	var active bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE finished_at = 0)`).Scan(&active); err != nil || active {
		return err
	}
	if s.db.dialect == "postgres" {
		for _, table := range []string{"run_output", "run_output_archives"} {
			if err := interrupt.Err(); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `VACUUM (SKIP_LOCKED) `+table); err != nil {
				return err
			}
		}
		return nil
	}
	var separateFiles bool
	if err := conn.QueryRowContext(ctx, `SELECT @@innodb_file_per_table`).Scan(&separateFiles); err != nil || !separateFiles {
		return err
	}
	for _, table := range []string{"run_output", "run_output_archives"} {
		if err := interrupt.Err(); err != nil {
			return err
		}
		var free int64
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(DATA_FREE, 0) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table).Scan(&free); err != nil {
			return err
		}
		if free < 1024*1024 {
			continue
		}
		if err := interrupt.Err(); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `ALTER TABLE `+table+` NOWAIT FORCE, ALGORITHM=INPLACE, LOCK=NONE`); err != nil {
			return err
		}
	}
	return nil
}

// Cancel the server statement and wait for its normal response, including any
// DDL rollback, rather than closing its socket and admitting a job prematurely.
func (s *Store) interruptServerReclamation(ctx, interrupt context.Context, conn *sql.Conn) (func(), error) {
	query := `SELECT pg_backend_pid()`
	if s.db.dialect == "mariadb" {
		query = `SELECT CONNECTION_ID()`
	}
	var id int64
	if err := conn.QueryRowContext(ctx, query).Scan(&id); err != nil {
		return nil, err
	}
	query = `SELECT pg_cancel_backend(` + strconv.FormatInt(id, 10) + `)`
	if s.db.dialect == "mariadb" {
		query = `KILL QUERY ` + strconv.FormatInt(id, 10)
	}
	done, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-interrupt.Done():
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			default:
			}
			// A separate bounded connection avoids waiting on the busy session.
			// Retry to cover cancellation just before statement dispatch.
			attempt, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			_, _ = s.db.DB.ExecContext(attempt, query)
			cancel()
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		close(done)
		<-joined
	}, nil
}
