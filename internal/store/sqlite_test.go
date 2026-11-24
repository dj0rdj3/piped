package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestSQLiteConnectionPolicy(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "data", "piped.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	// Force the pool to create a new connection for every operation. Connection
	// replacement must retain foreign keys, durability, and the busy timeout.
	s.db.SetMaxIdleConns(0)
	for name, want := range map[string]string{
		"foreign_keys": "1", "busy_timeout": "5000",
		"journal_mode": "wal", "synchronous": "2",
	} {
		var got string
		if err := s.db.QueryRow("PRAGMA " + name).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %q, want %q: %v", name, got, want, err)
		}
	}
	if _, err := s.db.Exec("INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES('orphan',999,0,1)"); err == nil {
		t.Fatal("foreign key constraint was disabled")
	}
	var result any
	err = s.db.QueryRow("SELECT load_extension('piped-forbidden-extension')").Scan(&result)
	if err == nil || (!strings.Contains(err.Error(), "not authorized") && !strings.Contains(err.Error(), "no such function")) {
		t.Fatalf("dynamic extension loading was not disabled: %v", err)
	}
}

func TestSQLiteImmediateTransactions(t *testing.T) {
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "data", "piped.db")
	first, err := Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// Avoid a five-second test delay while preserving the production timeout.
	if _, err := second.db.Exec("PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	tx, err := first.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	other, err := second.db.BeginTx(ctx, nil)
	if err == nil {
		other.Rollback()
		t.Fatal("second writer began while an immediate transaction held the lock")
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_BUSY {
		t.Fatalf("expected SQLite busy error, got %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	other, err = second.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("writer lock was not released: %v", err)
	}
	if err := other.Commit(); err != nil {
		t.Fatal(err)
	}
}
