package execution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"piped/internal/store"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var readOnlyReclaimerPath atomic.Pointer[string]

func init() {
	// Register before any test connections exist. The atomic, exact-path guard
	// restricts fault injection to this fixture's maintenance connection.
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
		path := readOnlyReclaimerPath.Load()
		if path == nil {
			return nil
		}
		uri, err := url.Parse(dsn)
		if err != nil || uri.Path != *path || !slices.Contains(uri.Query()["_pragma"], "busy_timeout(0)") {
			return nil
		}
		_, err = conn.ExecContext(context.Background(), `PRAGMA query_only = ON`, nil)
		return err
	})
}

func TestFailedSQLiteStartupConversionPreservesAllExistingData(t *testing.T) {
	if os.Getenv("PIPED_TEST_DATABASE_CONFIG") != "" {
		t.Skip("SQLite startup conversion")
	}
	f := newFixture(t, "sequential", false)
	f.engine.Close()
	ctx := context.Background()
	runAdmission, err := f.db.AdmitRun(ctx, f.actor, store.RunAdmission{ID: "startup-preservation", RequestID: "startup-preservation", JobID: f.job.ID, Version: f.job.Version})
	run := runAdmission.PublicRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.AppendOutput(ctx, run.ID, f.hosts[0].ID, []byte("existing output\x00\xe2\x82\xac\n")); err != nil {
		t.Fatal(err)
	}
	inspection, err := sql.Open("sqlite", f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	inspection.SetMaxOpenConns(1)
	if _, err := inspection.ExecContext(ctx, `PRAGMA auto_vacuum = NONE`); err != nil {
		t.Fatal(err)
	}
	if _, err := inspection.ExecContext(ctx, `VACUUM`); err != nil {
		t.Fatal(err)
	}
	before := snapshotSQLiteTables(t, inspection)
	readOnlyReclaimerPath.Store(&f.databasePath)
	defer readOnlyReclaimerPath.Store(nil)
	failed, err := New(ctx, f.db, f.vault, slog.New(slog.NewTextHandler(io.Discard, nil)), 7)
	if failed != nil {
		failed.Close()
		t.Fatal("startup succeeded despite forced conversion failure")
	}
	var sqliteErr *sqlite.Error
	if err == nil || !strings.Contains(err.Error(), "prepare SQLite space reclamation") || !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_READONLY {
		t.Fatalf("did not exercise a conversion failure: %v", err)
	}
	if !bytes.Equal(before, snapshotSQLiteTables(t, inspection)) {
		t.Fatal("failed startup changed existing rows, credentials, sessions, output, or migration history")
	}
	var mode int
	if err := inspection.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode != 0 {
		t.Fatalf("failed conversion changed the database format: %d %v", mode, err)
	}
	preserved, err := f.db.Run(ctx, f.actor, run.ID)
	if err != nil || preserved.Status != "running" || preserved.FinishedAt != 0 {
		t.Fatalf("failed startup recovered an existing run: %+v %v", preserved, err)
	}
	for _, host := range preserved.Hosts {
		if host.Status != "queued" || host.FinishedAt != 0 {
			t.Fatalf("failed startup changed an existing host: %+v", host)
		}
	}
	t.Log("Forced read-only startup conversion failed; every table, run/host status, session, output cursor, and database format remained unchanged")
}

func snapshotSQLiteTables(t *testing.T, db *sql.DB) []byte {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string][][]any)
	for _, name := range names {
		snapshot[name] = nil
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for index := range targets {
				targets[index] = &values[index]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			for index, value := range values {
				if data, ok := value.([]byte); ok {
					values[index] = bytes.Clone(data)
				}
			}
			snapshot[name] = append(snapshot[name], values)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
