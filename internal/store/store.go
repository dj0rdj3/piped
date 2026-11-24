// Package store owns database initialization, migrations, and persistent accounts.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql migrations/mysql/*.sql
var migrations embed.FS

var (
	ErrNotFound     = errors.New("record not found")
	ErrConflict     = errors.New("username already exists")
	ErrInitialized  = errors.New("administrator already initialized")
	ErrUnauthorized = errors.New("session or credentials changed")
	ErrForbidden    = errors.New("API key does not permit this operation")
	ErrLastUser     = errors.New("cannot delete the last user")
)

type Store struct {
	db             *database
	runListenersMu sync.Mutex
	runListeners   map[runWatchKey]map[chan struct{}]struct{}
}

func Open(ctx context.Context, filename string) (*Store, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("database directory must be private (chmod 700)")
	}
	if info, err := os.Lstat(abs); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("database must be a regular file, not a symlink")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	// A single connection serializes SQLite writes for this small-team workload.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: &database{DB: db, dialect: "sqlite"}}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users)").Scan(&exists)
	return exists, err
}

func (s *Store) migrate(ctx context.Context) error {
	if s.db.dialect != "sqlite" {
		return s.migrateServer(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.applyMigrations(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
