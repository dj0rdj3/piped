package execution

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/store"
	"piped/internal/testdatabase"
)

func TestStalledDatabaseCannotPinExecutionShutdown(t *testing.T) {
	driver, dsn := testdatabase.Settings(t)
	if driver == "sqlite" {
		t.Skip("requires disposable network database")
	}
	proxyDSN, stall, resume, blocked := testdatabase.StallingProxy(t, driver, dsn, "")
	defer resume()
	f := newFixtureWithDatabase(t, "parallel", false, store.Config{Driver: driver, DSN: proxyDSN})
	started := make(chan struct{}, len(f.hosts))
	write := make(chan struct{})
	f.engine.runner = func(_ context.Context, _ store.Connection, _ string, output io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		started <- struct{}{}
		<-write
		_, err := output.Write([]byte("output during a network stall"))
		return nil, err
	}
	startFixture(t, f)
	for range f.hosts {
		<-started
	}
	stall()
	close(write)
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("did not stall an established database reply")
	}
	closed := make(chan struct{})
	go func() { f.engine.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2*persistenceTimeout + 3*time.Second):
		t.Fatal("database network stall prevented joined shutdown")
	}
	resume()
	owner, err := f.db.AcquireExecutionOwner(context.Background())
	if err != nil {
		t.Fatal("shutdown retained database ownership", err)
	}
	owner.Close()
}

func TestOwnershipConnectionLossStopsExecution(t *testing.T) {
	driver, dsn := testdatabase.Settings(t)
	if driver == "sqlite" {
		t.Skip("server session ownership")
	}
	f := newFixtureWithDatabase(t, "parallel", false, store.Config{Driver: driver, DSN: dsn})
	if err := f.engine.owner.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.engine.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("server continued after losing ownership")
	}
	if !f.engine.ownershipLost.Load() {
		t.Fatal("ownership loss did not prevent finalization under a replacement owner")
	}
	if f.engine.Err() != ErrOwnershipLost {
		t.Fatal("ownership loss would be reported as a successful server shutdown")
	}
	if _, err := f.engine.Start(context.Background(), f.actor, f.job.ID, f.job.Version); err != ErrInactive {
		t.Fatal("ownership loss still permits admission", err)
	}
}

func TestKilledOwnershipSessionFencesAdmissionBeforeHeartbeat(t *testing.T) {
	driver, dsn := testdatabase.Settings(t)
	if driver == "sqlite" {
		t.Skip("requires a disposable SQL server session")
	}
	f := newFixtureWithDatabase(t, "parallel", false, store.Config{Driver: driver, DSN: dsn})
	started := make(chan struct{}, len(f.hosts))
	var running atomic.Int32
	f.engine.runner = func(ctx context.Context, _ store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		running.Add(1)
		defer running.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		// Model remote cleanup after cancellation, before releasing ownership.
		time.Sleep(100 * time.Millisecond)
		return nil, ctx.Err()
	}
	run := startFixture(t, f)
	for range f.hosts {
		<-started
	}
	sqlDriver := "mysql"
	if driver == "postgres" {
		sqlDriver = "pgx"
	}
	admin, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		t.Fatal("open disposable SQL control connection")
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if driver == "postgres" {
		var killed bool
		err = admin.QueryRowContext(ctx, `SELECT pg_terminate_backend(pid, 1000) FROM pg_locks
			WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
			AND classid::bigint * 4294967296 + objid::bigint = 7069706565`).Scan(&killed)
		if err != nil || !killed {
			t.Fatal("could not terminate the real PostgreSQL ownership session", err)
		}
	} else {
		var session int64
		if err := admin.QueryRowContext(ctx, `SELECT IS_USED_LOCK(SHA2(CONCAT(DATABASE(), '.piped.execution'), 256))`).Scan(&session); err != nil {
			t.Fatal("find disposable ownership session", err)
		}
		if _, err := admin.ExecContext(ctx, "KILL CONNECTION "+strconv.FormatInt(session, 10)); err != nil {
			t.Fatal("terminate disposable ownership session", err)
		}
	}
	// No heartbeat wait: both generation and live SQL lock are checked while
	// holding admission's writer lock. A replacement need not have claimed yet.
	_, err = f.engine.Start(context.Background(), f.actor, f.job.ID, f.job.Version)
	if !errors.Is(err, ErrOwnershipLost) && !errors.Is(err, ErrInactive) {
		t.Fatal("stale engine admitted work after its actual SQL session died", err)
	}
	if runs := schedulerRuns(t, f); len(runs) != 1 {
		t.Fatal("stale admission left another run", len(runs))
	}
	replacement, err := New(context.Background(), f.db, f.vault, nil, 7)
	if err != nil {
		t.Fatal("replacement could not acquire released SQL ownership", err)
	}
	defer replacement.Close()
	if running.Load() != 0 {
		t.Fatal("replacement recovered history before the former scripts drained")
	}
	if current := finished(t, f, run.ID); current.Status != "failed" {
		t.Fatal("replacement did not recover the abandoned run", current)
	}
	stale := f.engine.owner.Context(context.Background())
	for _, write := range []func() error{
		func() error { return f.db.SetRunHost(stale, run.ID, f.hosts[0].ID, "running", "", nil) },
		func() error { return f.db.FinishRun(stale, run.ID, "succeeded") },
		func() error { return f.engine.owner.MarkStopped(context.Background()) },
	} {
		if err := write(); !errors.Is(err, store.ErrExecutionOwnershipLost) {
			t.Fatal("old owner could write or clear the replacement's generation", err)
		}
	}
	if current := finished(t, f, run.ID); current.Status != "failed" {
		t.Fatal("stale completion overwrote replacement recovery", current)
	}
}

func TestCompressionDeadlineRecoversWithHealthyOwnershipConnection(t *testing.T) {
	driver, dsn := testdatabase.Settings(t)
	if driver != "postgres" {
		t.Skip("PostgreSQL queries require context deadlines after connecting")
	}
	proxyDSN, stall, resume, blocked := testdatabase.StallingProxy(t, driver, dsn, "SELECT h.run_id, h.host_id FROM run_hosts h")
	defer resume()
	f := newFixtureWithDatabase(t, "parallel", false, store.Config{Driver: driver, DSN: proxyDSN})
	var logs persistenceLogs
	f.engine.Close()
	engine, err := New(context.Background(), f.db, f.vault, slog.New(slog.NewTextHandler(&logs, nil)), 7)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = engine
	t.Cleanup(engine.Close)
	// Only a compression query stalls. Ownership and other pooled sessions stay
	// live even if initial retention/maintenance is still running.
	stall()
	select {
	case engine.archiveReady <- struct{}{}:
	default:
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("compression did not reach the selected network stall")
	}
	await(t, func() bool {
		return strings.Contains(logs.String(), "execution log compression failed")
	})
	select {
	case <-engine.Done():
		t.Fatal("test killed ownership instead of timing out background storage")
	default:
	}
	resume()
	engine.runner = func(_ context.Context, _ store.Connection, _ string, output io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		_, err := io.WriteString(output, "archive after restored database")
		code := 0
		return &code, err
	}
	run := startFixture(t, f)
	if current := finished(t, f, run.ID); current.Status != "succeeded" {
		t.Fatal("timed out maintenance retained the writer lock or blocked execution", current)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("open disposable compression check")
	}
	defer admin.Close()
	await(t, func() bool {
		var count int
		if err := admin.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM run_output_archives WHERE run_id = $1`, run.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count > 0
	})
}
