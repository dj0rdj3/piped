package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

var ErrExecutionOwned = errors.New("another Piped server owns this database")
var ErrExecutionOwnershipLost = errors.New("execution database ownership was lost")

type executionOwnerContextKey struct{}

// ExecutionOwner is held until all execution workers have stopped. CLI access
// and ordinary transactions do not take this lock. A pinned server connection
// must never return to the pool while it might still hold a session lock.
type ExecutionOwner struct {
	database   *database
	token      string
	sessionID  int64
	claimed    atomic.Bool
	closed     atomic.Bool
	lost       chan struct{}
	lostOnce   sync.Once
	connection *sql.Conn
	release    func() error
	once       sync.Once
	err        error
}

func (s *Store) AcquireExecutionOwner(ctx context.Context) (*ExecutionOwner, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.db.dialect == "sqlite" {
		uri, err := url.Parse(s.sqliteDSN)
		if err != nil {
			return nil, err
		}
		release, err := lockSQLiteExecution(uri.Path)
		if err != nil {
			return nil, err
		}
		return &ExecutionOwner{database: s.db, release: release, lost: make(chan struct{})}, nil
	}
	conn, err := s.db.DB.Conn(ctx)
	if err != nil {
		return nil, errors.New("acquire execution ownership connection failed")
	}
	unlock := `SELECT pg_advisory_unlock(7069706565)`
	if s.db.dialect != "postgres" {
		unlock = `SELECT RELEASE_LOCK(SHA2(CONCAT(DATABASE(), '.piped.execution'), 256))`
	}
	owner := &ExecutionOwner{database: s.db, connection: conn, lost: make(chan struct{}), release: func() error {
		// Confirm healthy shutdown releases ownership before the next server
		// starts. The deadline also covers an unresponsive or lost session.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = conn.ExecContext(cleanup, unlock)
		cancel()
		// Discarding closes the actual connection and releases its session locks,
		// including after an ambiguous acquisition or a failed health check.
		return discardOwnershipConnection(conn)
	}}
	var acquired bool
	query := `SELECT pg_try_advisory_lock(7069706565)`
	if s.db.dialect != "postgres" {
		query = `SELECT GET_LOCK(SHA2(CONCAT(DATABASE(), '.piped.execution'), 256), 0)`
	}
	if err := conn.QueryRowContext(ctx, query).Scan(&acquired); err != nil {
		owner.Close()
		return nil, errors.New("acquire execution ownership failed")
	}
	if !acquired {
		owner.Close()
		return nil, ErrExecutionOwned
	}
	identity := `SELECT pg_backend_pid()`
	if s.db.dialect != "postgres" {
		identity = `SELECT CONNECTION_ID()`
	}
	if err := conn.QueryRowContext(ctx, identity).Scan(&owner.sessionID); err != nil {
		owner.Close()
		return nil, errors.New("read execution ownership session failed")
	}
	return owner, nil
}

func (o *ExecutionOwner) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, executionOwnerContextKey{}, o)
}

func (o *ExecutionOwner) Lost() <-chan struct{} { return o.lost }

func (o *ExecutionOwner) lose() error {
	o.lostOnce.Do(func() { close(o.lost) })
	return ErrExecutionOwnershipLost
}

// Claim rotates the generation under the same writer lock as admission and
// execution writes. A takeover cannot pass a previously validated transaction.
// Return whether unfinished work needs the old controller's bounded drain time.
func (o *ExecutionOwner) Claim(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := o.database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var previous string
	if err := tx.QueryRowContext(ctx, `SELECT token FROM execution_ownership WHERE id = 1`).Scan(&previous); err != nil {
		return false, err
	}
	var active bool
	if previous != "" {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE finished_at = 0)`).Scan(&active); err != nil {
			return false, err
		}
	}
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	o.token = hex.EncodeToString(random)
	if err := o.checkSession(ctx, tx); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_ownership SET token = ? WHERE id = 1`, o.token); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	o.claimed.Store(true)
	return active, nil
}

func (o *ExecutionOwner) checkSession(ctx context.Context, tx *transaction) error {
	if o.closed.Load() {
		return o.lose()
	}
	if o.database.dialect == "sqlite" {
		return nil
	}
	var held bool
	query := `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype = 'advisory'
		AND pid = ? AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		AND classid::bigint * 4294967296 + objid::bigint = 7069706565)`
	if o.database.dialect != "postgres" {
		query = `SELECT COALESCE(IS_USED_LOCK(SHA2(CONCAT(DATABASE(), '.piped.execution'), 256)), 0) = ?`
	}
	if err := tx.QueryRowContext(ctx, query, o.sessionID).Scan(&held); err != nil {
		return err
	}
	if !held {
		return o.lose()
	}
	return nil
}

func authorizeExecutionOwner(ctx context.Context, tx *transaction) error {
	owner, ok := ctx.Value(executionOwnerContextKey{}).(*ExecutionOwner)
	if !ok {
		return nil // Browser/CLI operations do not impersonate an execution owner.
	}
	if owner.database != tx.database || !owner.claimed.Load() || owner.closed.Load() {
		return owner.lose()
	}
	var token string
	if err := tx.QueryRowContext(ctx, `SELECT token FROM execution_ownership WHERE id = 1`).Scan(&token); err != nil {
		return err
	}
	if token != owner.token {
		return owner.lose()
	}
	return owner.checkSession(ctx, tx)
}

// MarkStopped is only called after workers join. An interrupted startup or lost
// session leaves the generation dirty so a replacement drains before recovery.
func (o *ExecutionOwner) MarkStopped(ctx context.Context) error {
	tx, err := o.database.BeginTx(o.Context(ctx), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE execution_ownership SET token = '' WHERE id = 1 AND token = ?`, o.token); err != nil {
		return err
	}
	return tx.Commit()
}

// Check detects loss of the dedicated session. database/sql cannot replace the
// connection behind a sql.Conn, so a successful check retains the original lock.
func (o *ExecutionOwner) Check(ctx context.Context) error {
	if o.closed.Load() {
		return o.lose()
	}
	if o.connection == nil {
		return ctx.Err()
	}
	var value int
	return o.connection.QueryRowContext(ctx, `SELECT 1`).Scan(&value)
}

func (o *ExecutionOwner) Close() error {
	return o.close(false)
}

// Discard closes the physical session without waiting for an unlock reply from
// an unavailable server. Its dirty generation remains for takeover recovery.
func (o *ExecutionOwner) Discard() error {
	return o.close(true)
}

func discardOwnershipConnection(connection *sql.Conn) error {
	_ = connection.Raw(func(any) error { return driver.ErrBadConn })
	err := connection.Close()
	if errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	return err
}

func (o *ExecutionOwner) close(discard bool) error {
	o.once.Do(func() {
		o.closed.Store(true)
		if discard && o.connection != nil {
			o.err = discardOwnershipConnection(o.connection)
		} else {
			o.err = o.release()
		}
	})
	return o.err
}
