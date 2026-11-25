package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// SQLiteReclaimer uses a separate, fail-fast connection so maintenance never
// occupies the foreground connection while waiting for a reader or writer.
// One background worker owns it and yields between bounded vacuum steps.
type SQLiteReclaimer struct {
	db         *sql.DB
	foreground *sql.DB
}

// NewSQLiteReclaimer must run before the execution engine admits jobs. Existing
// databases without pointer maps need a one-time VACUUM to enable incremental
// vacuum. Fresh databases already select this format before creating tables.
// CLI-only store opens never rebuild an existing database.
func (s *Store) NewSQLiteReclaimer(ctx context.Context) (*SQLiteReclaimer, error) {
	if s.db.dialect != "sqlite" {
		return nil, nil
	}
	uri, err := url.Parse(s.sqliteDSN)
	if err != nil {
		return nil, err
	}
	query := uri.Query()
	query.Del("_pragma")
	query.Add("_pragma", "busy_timeout(0)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	err = prepareSQLiteReclamation(ctx, conn)
	conn.Close()
	if err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteReclaimer{db: db, foreground: s.db.DB}, nil
}

// Keep the mode change and VACUUM on the same connection: the requested mode
// for an existing NONE database is connection-local until VACUUM commits it.
// This fail-fast connection also avoids waiting on another process's writer.
func prepareSQLiteReclamation(ctx context.Context, conn *sql.Conn) error {
	var mode int
	if err := conn.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return err
	}
	if mode != 2 {
		if _, err := conn.ExecContext(ctx, `PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
			return err
		}
		if mode == 0 {
			if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
				return err
			}
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
			return err
		}
		if mode != 2 {
			return errors.New("SQLite incremental vacuum was not enabled")
		}
	}
	return nil
}

func (r *SQLiteReclaimer) Close() error { return r.db.Close() }

// Step returns whether maintenance should retry soon. Busy databases and short
// deadlines defer work rather than failing jobs or waiting behind their writes.
func (r *SQLiteReclaimer) Step(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if r.foreground.Stats().InUse > 0 {
		return true, nil
	}
	batch, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	more, err := r.reclaim(batch)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var sqliteErr *sqlite.Error
	busy := errors.As(err, &sqliteErr) && (sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY || sqliteErr.Code()&0xff == sqlite3.SQLITE_LOCKED)
	if busy || batch.Err() != nil {
		return true, nil
	}
	return more, err
}

func (r *SQLiteReclaimer) reclaim(ctx context.Context) (bool, error) {
	var free int64
	if err := r.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil {
		return false, err
	}
	if free > 0 {
		// Fully step the pragma: SQLite can return one result row per page.
		rows, err := r.db.QueryContext(ctx, `PRAGMA incremental_vacuum(32)`)
		if err != nil {
			return false, err
		}
		for rows.Next() {
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return false, err
		}
	}
	if r.foreground.Stats().InUse > 0 {
		return true, nil
	}
	var busy, frames, checkpointed int
	// New writes can recreate a large WAL after the freelist has drained. Check
	// it on idle steps too, rather than remembering a completed checkpoint.
	// PASSIVE never waits for readers/writers. Only try truncating the WAL when
	// it is caught up; busy_timeout=0 also makes that attempt yield immediately.
	if err := r.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &frames, &checkpointed); err != nil {
		return false, err
	}
	if busy != 0 || frames != checkpointed || r.foreground.Stats().InUse > 0 {
		return true, nil
	}
	if err := r.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil {
		return false, err
	}
	return free > 32 || busy != 0, nil
}
