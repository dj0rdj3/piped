package execution

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/jobs"
	"piped/internal/store"
)

type persistenceLogs struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	deferred chan struct{}
}

func (logs *persistenceLogs) Write(data []byte) (int, error) {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if bytes.Contains(data, []byte("execution finalization deferred")) {
		select {
		case logs.deferred <- struct{}{}:
		default:
		}
	}
	return logs.buffer.Write(data)
}

func (logs *persistenceLogs) String() string {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	return logs.buffer.String()
}

func rejectPersistence(t *testing.T, f *engineFixture, table string) func() {
	t.Helper()
	driver, dsn := f.configuration.Driver, f.configuration.DSN
	if driver == "sqlite" {
		dsn = f.databasePath + "?_pragma=busy_timeout(5000)"
	} else if driver == "postgres" {
		driver = "pgx"
	} else {
		driver = "mysql"
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	predicate := "finished_at = 0"
	if table == "run_output" {
		predicate = "OCTET_LENGTH(data) = 0"
	}
	reject := "ALTER TABLE " + table + " ADD CONSTRAINT fixture_reject_write CHECK (" + predicate + ")"
	remove := "ALTER TABLE " + table + " DROP CONSTRAINT fixture_reject_write"
	if f.configuration.Driver == "mysql" {
		remove = "ALTER TABLE " + table + " DROP CHECK fixture_reject_write"
	}
	if driver == "sqlite" {
		reject = "CREATE TRIGGER fixture_reject_write BEFORE UPDATE ON " + table + " WHEN NEW.finished_at > 0 BEGIN SELECT RAISE(ABORT, 'fixture write failure'); END"
		if table == "run_output" {
			reject = "CREATE TRIGGER fixture_reject_write BEFORE INSERT ON run_output BEGIN SELECT RAISE(ABORT, 'fixture output failure'); END"
		}
		remove = "DROP TRIGGER fixture_reject_write"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, reject); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	restore := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := db.ExecContext(ctx, remove); err != nil {
				t.Error("remove fixture write failure", err)
			}
		})
	}
	t.Cleanup(restore)
	return restore
}

func TestSecondEngineCannotRecoverLiveRuns(t *testing.T) {
	f := newFixture(t, "parallel", false)
	started := make(chan struct{}, len(f.hosts))
	f.engine.runner = func(ctx context.Context, _ store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	run := startFixture(t, f)
	for range f.hosts {
		<-started
	}
	second, err := New(context.Background(), f.db, f.vault, nil, 7)
	if second != nil {
		second.Close()
	}
	if !errors.Is(err, store.ErrExecutionOwned) {
		t.Fatalf("second server acquired a live database: %v", err)
	}
	current, err := f.db.Run(context.Background(), f.actor, run.ID)
	if err != nil || current.Status != "running" || current.FinishedAt != 0 {
		t.Fatal("second server recovered live work", current, err)
	}
	for _, host := range current.Hosts {
		if host.FinishedAt != 0 {
			t.Fatal("second server changed a live host", host)
		}
	}
}

func TestCompletionPersistenceRetriesWithoutReplayingScripts(t *testing.T) {
	for _, table := range []string{"runs", "run_hosts"} {
		t.Run(table, func(t *testing.T) {
			f := newFixture(t, "parallel", false)
			job, err := jobService(t, f.db, f.vault).Save(context.Background(), f.actor, f.job.ID, jobs.Input{
				Name: f.job.Name, Script: f.job.Script, HostIDs: f.job.HostIDs,
				ExecutionMode: "parallel", Version: f.job.Version,
				Schedule: &store.JobSchedule{Cron: "* * * * *", Enabled: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			f.job = job
			logs := &persistenceLogs{deferred: make(chan struct{}, 1)}
			f.engine.logger = slog.New(slog.NewTextHandler(logs, nil))
			restore := rejectPersistence(t, f, table)
			var calls atomic.Int32
			f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
				calls.Add(1)
				code := 0
				return &code, nil
			}
			run := startFixture(t, f)
			select {
			case <-logs.deferred:
			case <-time.After(5 * time.Second):
				t.Fatal("did not exercise failed completion persistence")
			}
			future := time.Now().Truncate(time.Minute).Add(2 * time.Minute)
			_, _, _, err = f.db.CreateScheduledRun(context.Background(), "blocked-overlap", job.ID, job.Version, future.Unix(), func() time.Time { return future })
			if !errors.Is(err, store.ErrScheduleSkipped) {
				t.Fatal("pending completion allowed scheduled overlap", err)
			}
			f.engine.mu.Lock()
			tracked := f.engine.active[run.ID] != nil
			f.engine.mu.Unlock()
			if !tracked {
				t.Fatal("pending completion lost active tracking")
			}
			restore()
			current := finished(t, f, run.ID)
			if current.Status != "succeeded" || calls.Load() != int32(len(f.hosts)) {
				t.Fatal("persistence retry lost the outcome or replayed SSH", current, calls.Load())
			}
			for _, host := range current.Hosts {
				if host.Status != "succeeded" || host.FinishedAt == 0 {
					t.Fatal("pending host result was lost", host)
				}
			}
			future = future.Add(time.Minute)
			if _, _, _, err := f.db.CreateScheduledRun(context.Background(), "next-occurrence", job.ID, job.Version, future.Unix(), func() time.Time { return future }); err != nil {
				t.Fatal("recovered completion still blocks scheduling", err)
			}
		})
	}
}

func TestOutputPersistenceFailureIsFailedAndLogged(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "stop"}[stop], func(t *testing.T) {
			f := newFixture(t, "sequential", stop)
			var logs persistenceLogs
			f.engine.logger = slog.New(slog.NewTextHandler(&logs, nil))
			rejectPersistence(t, f, "run_output")
			var calls atomic.Int32
			f.engine.runner = func(_ context.Context, _ store.Connection, _ string, output io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
				calls.Add(1)
				_, err := output.Write([]byte("fixture output"))
				return nil, err
			}
			run := startFixture(t, f)
			current := finished(t, f, run.ID)
			f.engine.Close()
			if current.Status != "failed" || current.Hosts[0].Status != "failed" || current.Hosts[0].Message != "Execution output could not be saved" {
				t.Fatal("storage failure was not preserved", current)
			}
			if !strings.Contains(logs.String(), "host execution failed") {
				t.Fatal("storage failure suppressed the execution error log")
			}
			expected := int32(len(f.hosts))
			if stop {
				expected = 1
			}
			if calls.Load() != expected {
				t.Fatal("storage failure ignored stop-other-hosts policy", calls.Load())
			}
		})
	}
}

func TestShutdownBoundsFinalizationAndRestartPreservesCompletedHosts(t *testing.T) {
	f := newFixture(t, "parallel", false)
	restore := rejectPersistence(t, f, "runs")
	f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
		code := 0
		return &code, nil
	}
	run := startFixture(t, f)
	await(t, func() bool {
		current, err := f.db.Run(context.Background(), f.actor, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, host := range current.Hosts {
			if host.FinishedAt == 0 {
				return false
			}
		}
		return true
	})
	closed := make(chan struct{})
	go func() { f.engine.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(persistenceTimeout + 3*time.Second):
		restore()
		t.Fatal("shutdown waited indefinitely for completion storage")
	}
	restore()
	restarted, err := New(context.Background(), f.db, f.vault, nil, 7)
	if err != nil {
		t.Fatal("shutdown did not release ownership", err)
	}
	defer restarted.Close()
	current := finished(t, f, run.ID)
	if current.Status != "succeeded" {
		t.Fatal("restart replaced committed host outcomes", current)
	}
}

func TestCompletedHostRetriesWhileOtherScriptsAreRunning(t *testing.T) {
	f := newFixture(t, "parallel", false)
	logs := &persistenceLogs{deferred: make(chan struct{}, 1)}
	f.engine.logger = slog.New(slog.NewTextHandler(logs, nil))
	restore := rejectPersistence(t, f, "run_hosts")
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{}, len(f.hosts))
	var calls atomic.Int32
	f.engine.runner = func(ctx context.Context, connection store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		calls.Add(1)
		started <- struct{}{}
		if connection.Host.ID != f.hosts[0].ID {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		code := 0
		return &code, nil
	}
	run := startFixture(t, f)
	for range f.hosts {
		<-started
	}
	select {
	case <-logs.deferred:
	case <-time.After(5 * time.Second):
		t.Fatal("finished host did not encounter the injected persistence failure")
	}
	restore()
	await(t, func() bool {
		current, err := f.db.Run(context.Background(), f.actor, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.FinishedAt != 0 || current.Hosts[1].FinishedAt != 0 || current.Hosts[2].FinishedAt != 0 {
			t.Fatal("blocked scripts finished before their release")
		}
		return current.Hosts[0].Status == "succeeded" && current.Hosts[0].FinishedAt > 0
	})
	if calls.Load() != int32(len(f.hosts)) {
		t.Fatal("completion persistence replayed scripts", calls.Load())
	}
	// Release both remaining scripts and verify parent finalization still joins them.
	release <- struct{}{}
	release <- struct{}{}
	if current := finished(t, f, run.ID); current.Status != "succeeded" {
		t.Fatal("parent lost a previously persisted host result", current)
	}
}

func TestCancellationAndShutdownFlushMaskedOutputTail(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "individual_cancel", true: "engine_shutdown"}[shutdown], func(t *testing.T) {
			f := newFixture(t, "parallel", false)
			secret := "buffered-prefix-secret"
			job, err := jobService(t, f.db, f.vault).Save(context.Background(), f.actor, f.job.ID, jobs.Input{
				Name: f.job.Name, Script: f.job.Script, HostIDs: f.job.HostIDs,
				ExecutionMode: "parallel", Version: f.job.Version,
				Secrets: []jobs.SecretInput{{Name: "TOKEN", Value: &secret}},
			})
			if err != nil {
				t.Fatal(err)
			}
			f.job = job
			buffered := make(chan struct{}, len(f.hosts))
			f.engine.runner = func(ctx context.Context, _ store.Connection, _ string, output io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
				// The complete secret must stay redacted. The suffix remains buffered
				// until Close because it could become another occurrence of TOKEN.
				if _, err := io.WriteString(output, secret+"|buffered-prefix-"); err != nil {
					return nil, err
				}
				buffered <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			}
			run := startFixture(t, f)
			for range f.hosts {
				<-buffered
			}
			if shutdown {
				f.engine.Close()
			} else {
				for _, host := range f.hosts {
					if err := f.engine.Cancel(context.Background(), f.actor, run.ID, host.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if current := finished(t, f, run.ID); current.Status != "cancelled" {
				t.Fatal("cancellation result was not durable", current)
			}
			for _, host := range f.hosts {
				chunks, err := f.db.RunOutput(context.Background(), f.actor, run.ID, host.ID, 0)
				if err != nil {
					t.Fatal(err)
				}
				var output strings.Builder
				for _, chunk := range chunks {
					output.Write(chunk.Data)
				}
				if output.String() != "[REDACTED]|buffered-prefix-" {
					t.Fatal("masked output or its buffered tail was lost", output.String())
				}
			}
		})
	}
}
