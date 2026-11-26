package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"piped/internal/lifecycle"
)

func (s *Store) migrateServer(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return errors.New("connect for database migration failed")
	}
	defer conn.Close()
	// Session locks cover schema bootstrap before a transaction/lock row exists.
	// Hold the same lock in the CLI and server, using a dedicated connection.
	if s.db.dialect == "postgres" {
		_, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock(7069706564)")
	} else {
		var acquired int
		err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(SHA2(CONCAT(DATABASE(), '.piped.migrations'), 256), 25)").Scan(&acquired)
		if err == nil && acquired != 1 {
			err = errors.New("migration lock unavailable")
		}
	}
	if err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return errors.New("acquire database migration lock failed")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), lifecycle.StorageTimeout)
		defer cancel()
		query := "SELECT pg_advisory_unlock(7069706564)"
		if s.db.dialect != "postgres" {
			query = "SELECT RELEASE_LOCK(SHA2(CONCAT(DATABASE(), '.piped.migrations'), 256))"
		}
		if _, err := conn.ExecContext(cleanup, query); err != nil {
			// Never return a pooled connection still holding a session lock.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if s.db.dialect == "postgres" {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := s.applyMigrations(ctx, tx); err != nil {
			return err
		}
		return tx.Commit()
	}
	// MariaDB/MySQL DDL commits implicitly. Mark each version pending before DDL
	// and fail closed on an interrupted/partial migration, never pretending it ran.
	return s.applyMigrations(ctx, conn)
}

type migrationConnection interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// History validation and execution are shared; schema files and lock/transaction
// lifetimes remain specific to each database family.
func (s *Store) applyMigrations(ctx context.Context, conn migrationConnection) error {
	directory := "migrations/" + s.db.dialect
	history := "CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT PRIMARY KEY, checksum VARCHAR(64) NOT NULL)"
	switch s.db.dialect {
	case "sqlite":
		history = "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, checksum TEXT NOT NULL)"
	case "mysql", "mariadb":
		directory = "migrations/mysql"
		history += " ENGINE=InnoDB"
	}
	exec := func(query string, args ...any) error {
		_, err := conn.ExecContext(ctx, s.db.query(query), args...)
		return err
	}
	if err := exec(history); err != nil {
		return errors.New("create database migration history failed")
	}
	entries, err := migrations.ReadDir(directory)
	if err != nil {
		return err
	}
	checksums := make([]string, len(entries))
	bodies := make([][]byte, len(entries))
	for index, entry := range entries {
		version, err := strconv.Atoi(strings.Split(entry.Name(), "_")[0])
		if err != nil || version != index+1 {
			return errors.New("invalid database migration sequence")
		}
		bodies[index], err = migrations.ReadFile(directory + "/" + entry.Name())
		if err != nil {
			return err
		}
		checksums[index] = fmt.Sprintf("%x", sha256.Sum256(bodies[index]))
	}
	rows, err := conn.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	defer rows.Close()
	applied := []string{}
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return err
		}
		if version != len(applied)+1 || version > len(entries) {
			return errors.New("database migration history has gaps or is newer than this binary")
		}
		applied = append(applied, checksum)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Validate the entire existing history before applying any new DDL.
	if len(applied) > len(checksums) || !slices.Equal(applied, checksums[:len(applied)]) {
		return errors.New("database migration checksum mismatch or incomplete migration; restore a backup or reset the development database")
	}
	for index := len(applied); index < len(bodies); index++ {
		body := bodies[index]
		version, checksum := index+1, checksums[index]
		if err := exec("INSERT INTO schema_migrations(version, checksum) VALUES (?, 'pending')", version); err != nil {
			return err
		}
		// Preserve SQLite's full SQL batches. Server files have no semicolons
		// inside literals and execute separately with multi-statement mode off.
		statements := []string{string(body)}
		if s.db.dialect != "sqlite" {
			statements = strings.Split(string(body), ";")
		}
		for _, statement := range statements {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if err := exec(statement); err != nil {
				return fmt.Errorf("database migration %d failed: %w", version, err)
			}
		}
		if err := exec("UPDATE schema_migrations SET checksum = ? WHERE version = ?", checksum, version); err != nil {
			return err
		}
	}
	return nil
}
