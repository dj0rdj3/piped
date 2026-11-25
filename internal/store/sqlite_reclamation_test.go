package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestSQLiteReclaimerYieldsToWritersAndBoundsEachStep(t *testing.T) {
	db, actor := archiveFixture(t)
	if db.db.dialect != "sqlite" {
		reclaimer, err := db.NewSQLiteReclaimer(context.Background())
		if err != nil || reclaimer != nil {
			t.Fatal("server database created a SQLite maintenance connection", err)
		}
		return
	}
	ctx := context.Background()
	for range 32 {
		if err := db.AppendOutput(ctx, "archive", "host", bytes.Repeat([]byte{'x'}, MaxOutputBatchBytes)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetRunHost(ctx, "archive", "host", "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CompressRunOutput(ctx, "archive", "host"); err != nil {
		t.Fatal(err)
	}
	reclaimer, err := db.NewSQLiteReclaimer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reclaimer.Close()
	// A held foreground transaction must not queue maintenance on that pool.
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	started := time.Now()
	more, err := reclaimer.Step(ctx)
	if err != nil || !more || time.Since(started) > time.Second {
		t.Fatalf("maintenance waited behind the foreground connection: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// An unrelated process's writer is invisible to foreground pool statistics.
	other, err := Open(ctx, dbFilename(t, db))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherTx, err := other.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer otherTx.Rollback()
	started = time.Now()
	more, err = reclaimer.Step(ctx)
	if err != nil || !more || time.Since(started) > time.Second {
		t.Fatalf("maintenance waited on another writer: %v", err)
	}
	if err := otherTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := db.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := reclaimer.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if reclaimed := before - after; reclaimed < 1 || reclaimed > 32 {
		t.Fatalf("unbounded or ineffective vacuum step: %d pages", reclaimed)
	}
	for more := true; more; {
		more, err = reclaimer.Step(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&after); err != nil || after != 0 {
		t.Fatalf("maintenance left reusable pages instead of reclaiming them: %d %v", after, err)
	}
	chunks, _, err := db.RunOutputTail(ctx, actor, "archive", "host")
	if err != nil || len(chunks) == 0 {
		t.Fatal("reclamation damaged output", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := reclaimer.Step(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("maintenance ignored cancellation: %v", err)
	}
	// Connection replacement must retain fail-fast locking and full durability.
	reclaimer.db.SetMaxIdleConns(0)
	for pragma, want := range map[string]int{"busy_timeout": 0, "foreign_keys": 1, "synchronous": 2, "auto_vacuum": 2} {
		var got int
		if err := reclaimer.db.QueryRowContext(ctx, `PRAGMA `+pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("maintenance connection %s=%d, want %d: %v", pragma, got, want, err)
		}
	}
}

func dbFilename(t *testing.T, db *Store) string {
	t.Helper()
	var sequence int
	var name, filename string
	if err := db.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &filename); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestSQLiteReclaimerTruncatesNewWALAfterVacuumFinishes(t *testing.T) {
	db, _ := archiveFixture(t)
	if db.db.dialect != "sqlite" {
		t.Skip("SQLite WAL reclamation")
	}
	ctx := context.Background()
	reclaimer, err := db.NewSQLiteReclaimer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reclaimer.Close()
	for more := true; more; {
		more, err = reclaimer.Step(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	// New job output creates a WAL after the previous vacuum/checkpoint drained.
	// No deletions occur, so freelist_count stays zero throughout this cycle.
	for range 8 {
		if err := db.AppendOutput(ctx, "archive", "host", bytes.Repeat([]byte{'x'}, MaxOutputBatchBytes)); err != nil {
			t.Fatal(err)
		}
	}
	var free int
	if err := db.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil || free != 0 {
		t.Fatalf("fixture unexpectedly has free pages: %d %v", free, err)
	}
	wal := dbFilename(t, db) + "-wal"
	before, err := os.Stat(wal)
	if err != nil || before.Size() == 0 {
		t.Fatal("fixture did not create a new WAL", err)
	}
	for more := true; more; {
		more, err = reclaimer.Step(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(wal)
	if err != nil || after.Size() != 0 {
		t.Fatal("idle maintenance left a new WAL allocated after vacuum finished", err)
	}
}

func TestSQLiteReclamationConvertsExistingFilesOnlyAtServerPreparation(t *testing.T) {
	db, actor := archiveFixture(t)
	if db.db.dialect != "sqlite" {
		t.Skip("SQLite file format conversion")
	}
	ctx := context.Background()
	if err := db.AppendOutput(ctx, "archive", "host", []byte("preserve output and cursor")); err != nil {
		t.Fatal(err)
	}
	beforeRun, err := db.Run(ctx, actor, "archive")
	if err != nil {
		t.Fatal(err)
	}
	beforeOutput := readAllOutput(t, db, actor)
	// Model an existing database created before incremental vacuum was enabled.
	if _, err := db.db.ExecContext(ctx, `PRAGMA auto_vacuum = NONE`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `VACUUM`); err != nil {
		t.Fatal(err)
	}
	filename := dbFilename(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var mode int
	if err := reopened.db.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode != 0 {
		t.Fatalf("a CLI-style open rebuilt an existing file: mode=%d err=%v", mode, err)
	}
	writer, err := reopened.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	blocked, cancel := context.WithTimeout(ctx, time.Second)
	failed, err := reopened.NewSQLiteReclaimer(blocked)
	cancel()
	if failed != nil {
		failed.Close()
		t.Fatal("startup conversion succeeded despite another writer")
	}
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup conversion waited behind a busy foreground pool: %v", err)
	}
	if err := writer.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode != 0 {
		t.Fatalf("failed conversion changed the database format: mode=%d err=%v", mode, err)
	}
	// Conversion must remain correct when the foreground pool replaces connections.
	reopened.db.SetMaxIdleConns(0)
	reclaimer, err := reopened.NewSQLiteReclaimer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reclaimer.Close()
	if err := reopened.db.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode != 2 {
		t.Fatalf("incremental vacuum was not enabled: mode=%d err=%v", mode, err)
	}
	afterRun, err := reopened.Run(ctx, actor, "archive")
	if err != nil || !reflect.DeepEqual(beforeRun, afterRun) || !reflect.DeepEqual(beforeOutput, readAllOutput(t, reopened, actor)) {
		t.Fatal("conversion changed records, sessions, or output cursors", err)
	}
	if err := reopened.migrate(ctx); err != nil {
		t.Fatal("conversion changed migration history", err)
	}
	info, err := os.Stat(filename)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("conversion changed database permissions", err)
	}
}

func TestSQLiteReclaimerDefersWALTruncationForPinnedReaders(t *testing.T) {
	db, actor := archiveFixture(t)
	if db.db.dialect != "sqlite" {
		t.Skip("SQLite WAL readers")
	}
	ctx := context.Background()
	for range 8 {
		if err := db.AppendOutput(ctx, "archive", "host", bytes.Repeat([]byte{'x'}, MaxOutputBatchBytes)); err != nil {
			t.Fatal(err)
		}
	}
	reclaimer, err := db.NewSQLiteReclaimer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reclaimer.Close()
	reader, err := reclaimer.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, `BEGIN DEFERRED`); err != nil {
		t.Fatal(err)
	}
	defer reader.ExecContext(context.Background(), `ROLLBACK`)
	var count int
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_output`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	// Return the connection only after the pinned read is released. Use another
	// reclaimer to verify it never waits behind that connection or snapshot.
	other, err := db.NewSQLiteReclaimer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := db.SetRunHost(ctx, "archive", "host", "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CompressRunOutput(ctx, "archive", "host"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if more, err := other.Step(ctx); err != nil || !more || time.Since(started) > time.Second {
		t.Fatalf("checkpoint waited for a pinned reader: more=%v err=%v", more, err)
	}
	if _, _, err := db.RunOutputTail(ctx, actor, "archive", "host"); err != nil {
		t.Fatal("maintenance blocked foreground reads", err)
	}
	if _, err := reader.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	for more := true; more; {
		more, err = other.Step(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	wal, err := os.Stat(dbFilename(t, db) + "-wal")
	if err != nil || wal.Size() != 0 {
		t.Fatal("checkpoint did not reclaim the WAL after the reader released its snapshot", err)
	}
}
