package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/auth"
	"piped/internal/hosts"
	"piped/internal/jobs"
	"piped/internal/secrets"
	"piped/internal/store"
	"piped/internal/testdatabase"
)

type engineFixture struct {
	configuration store.Config
	databasePath  string
	engine        *Engine
	db            *store.Store
	vault         *secrets.Vault
	actor         store.Actor
	hosts         []store.Host
	job           store.Job
}

func newFixture(t *testing.T, mode string, stop bool) *engineFixture {
	t.Helper()
	driver, dsn := testdatabase.Settings(t)
	databasePath := filepath.Join(t.TempDir(), "data", "test.db")
	return newFixtureWithDatabase(t, mode, stop, store.Config{Driver: driver, DSN: dsn, Path: databasePath})
}

func newFixtureWithDatabase(t *testing.T, mode string, stop bool, configuration store.Config) *engineFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenDatabase(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := db.InitAdmin(ctx, "admin", "fixture-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, user, "session", time.Now().Unix(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{User: user, SessionHash: "session"}
	vault, err := secrets.New(bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := hosts.New(ctx, db, vault)
	if err != nil {
		t.Fatal(err)
	}
	targets := []store.Host{}
	for _, name := range []string{"First", "Second", "Third"} {
		password := "fixture-ssh-secret"
		target, err := service.Save(ctx, actor, "", hosts.Input{Name: name, Address: "127.0.0.1", Port: 22, Username: "fixture", Password: &password})
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, target)
	}
	job, err := jobService(t, db, vault).Save(ctx, actor, "", jobs.Input{Name: "Fixture job", Script: "printf 'hello\\n'", HostIDs: []string{targets[0].ID, targets[1].ID, targets[2].ID}, ExecutionMode: mode, StopOnFailure: stop})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := New(ctx, db, vault, slog.New(slog.NewTextHandler(io.Discard, nil)), 7)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	return &engineFixture{
		configuration: configuration,
		databasePath:  configuration.Path,
		engine:        engine,
		db:            db,
		vault:         vault,
		actor:         actor,
		hosts:         targets,
		job:           job,
	}
}

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not complete")
}
func finished(t *testing.T, f *engineFixture, id string) store.Run {
	t.Helper()
	var run store.Run
	await(t, func() bool {
		var err error
		run, err = f.db.Run(context.Background(), f.actor, id)
		if err != nil {
			t.Fatal(err)
		}
		return run.FinishedAt > 0
	})
	return run
}
func startFixture(t *testing.T, f *engineFixture) store.Run {
	t.Helper()
	run, err := f.engine.Start(context.Background(), f.actor, StartRequest{JobID: f.job.ID, Version: f.job.Version, RequestID: rand.Text()})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestSequentialOrderAndSnapshot(t *testing.T) {
	f := newFixture(t, "sequential", false)
	release := make(chan struct{})
	defer close(release)
	started := make(chan string, 3)
	f.engine.runner = func(ctx context.Context, c store.Connection, script string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		if script != f.job.Script {
			t.Error("script snapshot changed")
		}
		started <- c.Host.ID
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		code := 0
		return &code, nil
	}
	run := startFixture(t, f)
	if first := <-started; first != f.hosts[0].ID {
		t.Fatal("target order changed")
	}
	select {
	case <-started:
		t.Fatal("sequential targets overlapped")
	case <-time.After(30 * time.Millisecond):
	}
	if err := jobService(t, f.db, f.vault).Delete(context.Background(), f.actor, f.job.ID, f.job.Version); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	if second := <-started; second != f.hosts[1].ID {
		t.Fatal("second target out of order")
	}
	release <- struct{}{}
	if third := <-started; third != f.hosts[2].ID {
		t.Fatal("third target out of order")
	}
	release <- struct{}{}
	result := finished(t, f, run.ID)
	if result.Status != "succeeded" || result.JobName != f.job.Name {
		t.Fatalf("history lost snapshot: %+v", result)
	}
}

func TestParallelFailureAndCancellationPolicies(t *testing.T) {
	for _, cancelHost := range []bool{false, true} {
		for _, stop := range []bool{false, true} {
			name := "failure"
			if cancelHost {
				name = "cancel"
			}
			if stop {
				name += "-stop"
			} else {
				name += "-continue"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t, "parallel", stop)
				started := make(chan string, 3)
				fail := make(chan struct{})
				release := make(chan struct{})
				defer close(release)
				f.engine.runner = func(ctx context.Context, c store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
					started <- c.Host.ID
					if c.Host.ID == f.hosts[0].ID && !cancelHost {
						select {
						case <-fail:
							code := 3
							return &code, errors.New("failure")
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-release:
						code := 0
						return &code, nil
					}
				}
				run := startFixture(t, f)
				for range 3 {
					select {
					case <-started:
					case <-time.After(2 * time.Second):
						t.Fatal("targets did not start in parallel")
					}
				}
				if cancelHost {
					if err := f.engine.Cancel(context.Background(), f.actor, HostRequest{RunID: run.ID, HostID: f.hosts[0].ID}); err != nil {
						t.Fatal(err)
					}
				} else {
					close(fail)
				}
				if !stop {
					await(t, func() bool { r, _ := f.db.Run(context.Background(), f.actor, run.ID); return r.Hosts[0].FinishedAt > 0 })
					r, _ := f.db.Run(context.Background(), f.actor, run.ID)
					if r.Hosts[1].FinishedAt != 0 || r.Hosts[2].FinishedAt != 0 {
						t.Fatal("continue policy cancelled other hosts")
					}
					release <- struct{}{}
					release <- struct{}{}
				}
				result := finished(t, f, run.ID)
				for _, host := range result.Hosts[1:] {
					expected := "succeeded"
					if stop {
						expected = "cancelled"
					}
					if host.Status != expected {
						t.Fatalf("host status: %+v", host)
					}
				}
				expected := "failed"
				if cancelHost {
					expected = "cancelled"
				}
				if result.Status != expected {
					t.Fatalf("run status: %s", result.Status)
				}
			})
		}
	}
}

func TestStoppingOneRunLeavesConcurrentRunsOnTheSameHostsActive(t *testing.T) {
	for _, cancelHost := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelHost), func(t *testing.T) {
			f := newFixture(t, "parallel", true)
			other, err := jobService(t, f.db, f.vault).Save(context.Background(), f.actor, "", jobs.Input{
				Name: "Independent job", Script: "echo independent", HostIDs: f.job.HostIDs,
				ExecutionMode: "parallel", StopOnFailure: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{}, 6)
			fail := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			f.engine.runner = func(ctx context.Context, c store.Connection, script string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
				started <- struct{}{}
				trigger := release
				if script == f.job.Script && c.Host.ID == f.hosts[0].ID && !cancelHost {
					trigger = fail
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-trigger:
					code := 0
					if script == f.job.Script {
						code = 3
						return &code, errors.New("fixture failure")
					}
					return &code, nil
				}
			}
			first := startFixture(t, f)
			second, err := f.engine.Start(context.Background(), f.actor, StartRequest{JobID: other.ID, Version: other.Version, RequestID: "other-intent"})
			if err != nil {
				t.Fatal(err)
			}
			for range 6 {
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("concurrent runs did not start")
				}
			}
			if cancelHost {
				if err := f.engine.Cancel(context.Background(), f.actor, HostRequest{RunID: first.ID, HostID: f.hosts[0].ID}); err != nil {
					t.Fatal(err)
				}
			} else {
				close(fail)
			}
			finished(t, f, first.ID)
			for range 3 {
				select {
				case release <- struct{}{}:
				case <-time.After(3 * time.Second):
					t.Fatal("the independent run stopped accepting work")
				}
			}
			if result := finished(t, f, second.ID); result.Status != "succeeded" {
				t.Fatalf("stopping a run affected another run on the same hosts: %+v", result)
			}
		})
	}
}

func TestParallelOutcomeDoesNotDependOnHostCompletionOrder(t *testing.T) {
	for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			f := newFixture(t, "parallel", false)
			started := make(chan struct{}, 3)
			releases := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
			f.engine.runner = func(ctx context.Context, c store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
				index := 0
				for f.hosts[index].ID != c.Host.ID {
					index++
				}
				started <- struct{}{}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-releases[index]:
				}
				code := []int{0, 130, 7}[index]
				if code == 0 {
					return &code, nil
				}
				return &code, errors.New("script exited")
			}
			run := startFixture(t, f)
			for range f.hosts {
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("hosts did not start")
				}
			}
			for _, index := range order {
				close(releases[index])
				await(t, func() bool {
					current, err := f.db.Run(context.Background(), f.actor, run.ID)
					if err != nil {
						t.Fatal(err)
					}
					return current.Hosts[index].FinishedAt != 0
				})
			}
			result := finished(t, f, run.ID)
			if result.Status != "failed" {
				t.Fatalf("completion order %v changed run outcome: %s", order, result.Status)
			}
			for index, want := range []string{"succeeded", "interrupted", "failed"} {
				if result.Hosts[index].Status != want {
					t.Fatalf("host %d status: %s, want %s", index, result.Hosts[index].Status, want)
				}
			}
			if err := f.engine.Cancel(context.Background(), f.actor, HostRequest{RunID: run.ID, HostID: f.hosts[0].ID}); !errors.Is(err, ErrInactive) {
				t.Fatalf("completed host accepted cancellation: %v", err)
			}
		})
	}
}

func TestSequentialFailureStopsQueuedHosts(t *testing.T) {
	f := newFixture(t, "sequential", true)
	var mu sync.Mutex
	calls := 0
	f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil, errors.New("failed")
	}
	run := finished(t, f, startFixture(t, f).ID)
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 || run.Hosts[1].Status != "cancelled" {
		t.Fatalf("failure did not stop queue: %d %+v", calls, run)
	}
}

func TestExecutionScopesRevocationAndStaleVersion(t *testing.T) {
	f := newFixture(t, "sequential", false)
	ctx := context.Background()
	key, token, err := auth.NewAPIKey(f.actor.User, "Author", []string{store.ScopeJobsWrite}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.CreateLocalAPIKey(ctx, f.actor.User, key, auth.TokenHash(token)); err != nil {
		t.Fatal(err)
	}
	actor, err := f.db.ResolveAPIKey(ctx, auth.TokenHash(token), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.Start(ctx, actor, StartRequest{JobID: f.job.ID, Version: 1, RequestID: "fixture-intent"}); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("author scope executed script: %v", err)
	}
	if _, err := f.engine.Start(ctx, f.actor, StartRequest{JobID: f.job.ID, Version: 2, RequestID: "fixture-intent"}); !errors.Is(err, store.ErrJobChanged) {
		t.Fatalf("stale version executed: %v", err)
	}
	key, token, err = auth.NewAPIKey(f.actor.User, "Runner", []string{store.ScopeRunsWrite}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.CreateLocalAPIKey(ctx, f.actor.User, key, auth.TokenHash(token)); err != nil {
		t.Fatal(err)
	}
	actor, err = f.db.ResolveAPIKey(ctx, auth.TokenHash(token), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.RevokeLocalAPIKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.Start(ctx, actor, StartRequest{JobID: f.job.ID, Version: 1, RequestID: "fixture-intent"}); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("revoked key executed: %v", err)
	}
	runs, err := f.db.Runs(ctx, f.actor, f.job.ID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("unauthorized runs persisted: %+v %v", runs, err)
	}
	if err := f.engine.Input(ctx, f.actor, InputRequest{RunID: "missing", HostID: "missing", Data: strings.Repeat("x", 4097)}); !errors.Is(err, ErrInput) {
		t.Fatal(err)
	}
	if err := f.engine.Cancel(ctx, f.actor, HostRequest{RunID: "missing", HostID: "missing"}); !errors.Is(err, ErrInactive) {
		t.Fatal(err)
	}
}

func TestOutputPaginationRetentionAndShutdown(t *testing.T) {
	f := newFixture(t, "parallel", false)
	started := make(chan struct{}, 3)
	payload := bytes.Repeat([]byte("x"), 4096*20)
	f.engine.runner = func(ctx context.Context, c store.Connection, _ string, output io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		if c.Host.ID == f.hosts[0].ID {
			if _, err := output.Write(payload); err != nil {
				return nil, err
			}
		}
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	run := startFixture(t, f)
	for range 3 {
		<-started
	}
	chunks, err := f.db.RunOutput(context.Background(), f.actor, run.ID, f.hosts[0].ID, 0)
	if err != nil || len(chunks) != 16 {
		t.Fatalf("bounded output: %d %v", len(chunks), err)
	}
	all := []byte{}
	for _, chunk := range chunks {
		all = append(all, chunk.Data...)
	}
	chunks, err = f.db.RunOutput(context.Background(), f.actor, run.ID, f.hosts[0].ID, chunks[len(chunks)-1].ID)
	if err != nil || len(chunks) != 4 {
		t.Fatalf("cursor: %d %v", len(chunks), err)
	}
	for _, chunk := range chunks {
		all = append(all, chunk.Data...)
	}
	if !bytes.Equal(all, payload) {
		t.Fatal("output changed")
	}
	if err := f.db.PurgeRuns(context.Background(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Run(context.Background(), f.actor, run.ID); err != nil {
		t.Fatal("active run purged")
	}
	f.engine.Close()
	result := finished(t, f, run.ID)
	if result.Status != "cancelled" {
		t.Fatal(result.Status)
	}
	if _, err := f.engine.Start(context.Background(), f.actor, StartRequest{JobID: f.job.ID, Version: 1, RequestID: "fixture-intent"}); !errors.Is(err, ErrInactive) {
		t.Fatal(err)
	}
	if err := f.db.PurgeRuns(context.Background(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Run(context.Background(), f.actor, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("completed run retained: %v", err)
	}
	runs, err := f.db.Runs(context.Background(), f.actor, f.job.ID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("retained runs: %v %v", runs, err)
	}
}

func TestInterruptedRunRecoveryAndCompletedFixtureDeletion(t *testing.T) {
	f := newFixture(t, "sequential", true)
	ctx := context.Background()
	runAdmission, err := f.db.AdmitRun(ctx, f.actor, store.RunAdmission{ID: strings.Repeat("a", 32), RequestID: "recovery-fixture", JobID: f.job.ID, Version: f.job.Version})
	run := runAdmission.PublicRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.DeleteCompletedRun(ctx, f.actor, run.ID); !errors.Is(err, store.ErrRunActive) {
		t.Fatalf("removed active execution: %v", err)
	}
	f.engine.Close()
	restarted, err := New(ctx, f.db, f.vault, nil, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, err := f.db.Run(ctx, f.actor, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "failed" || recovered.FinishedAt == 0 {
		t.Fatal("run not recovered as interrupted")
	}
	for _, target := range recovered.Hosts {
		if target.Status != "failed" || target.FinishedAt == 0 {
			t.Fatal("host not marked interrupted")
		}
	}
	if err := f.db.DeleteCompletedRun(ctx, f.actor, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Run(ctx, f.actor, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("fixture history was not removed")
	}
}

func TestCtrlCStatusPreservesFailurePolicyAndMixedOutcomes(t *testing.T) {
	for _, mode := range []string{"sequential", "parallel"} {
		for _, scenario := range []struct {
			name         string
			stop         bool
			otherFailure bool
			want         string
		}{
			{name: "continue", want: "interrupted"},
			{name: "stop", stop: true, want: "interrupted"},
			{name: "mixed failure", otherFailure: true, want: "failed"},
		} {
			t.Run(mode+"/"+scenario.name, func(t *testing.T) {
				f := newFixture(t, mode, scenario.stop)
				var logs bytes.Buffer
				f.engine.logger = slog.New(slog.NewTextHandler(&logs, nil))
				f.engine.runner = func(ctx context.Context, c store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
					if c.Host.ID == f.hosts[0].ID {
						code := 130
						return &code, errors.New("interrupted")
					}
					if scenario.stop {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					code := 0
					if scenario.otherFailure && c.Host.ID == f.hosts[1].ID {
						code = 7
						return &code, errors.New("script failed")
					}
					return &code, nil
				}
				run := startFixture(t, f)
				result := finished(t, f, run.ID)
				f.engine.Close()
				if scenario.otherFailure {
					if !strings.Contains(logs.String(), `msg="script exited"`) || !strings.Contains(logs.String(), "exit_code=7") || strings.Contains(logs.String(), "interrupted") {
						t.Fatalf("expected only the genuine failure in server logs: %s", logs.String())
					}
				} else if logs.Len() != 0 {
					t.Fatalf("manual interruption produced server logs: %s", logs.String())
				}
				if result.Status != scenario.want {
					t.Fatalf("run status = %s; want %s", result.Status, scenario.want)
				}
				first := result.Hosts[0]
				if first.Status != "interrupted" || first.FinishedAt == 0 || first.ExitCode == nil || *first.ExitCode != 130 {
					t.Fatalf("interruption not retained: %+v", first)
				}
				if scenario.stop {
					for _, host := range result.Hosts[1:] {
						if host.Status != "cancelled" {
							t.Fatalf("stop policy not applied: %+v", host)
						}
					}
				}
			})
		}
	}
}

func TestQueuedCancellationAppliesFailurePolicyImmediately(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "continue"
		if stop {
			name = "stop"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "sequential", stop)
			started := make(chan string, 3)
			release := make(chan struct{})
			defer close(release)
			f.engine.runner = func(ctx context.Context, c store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
				started <- c.Host.ID
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					code := 0
					return &code, nil
				}
			}
			run := startFixture(t, f)
			<-started
			if err := f.engine.Cancel(context.Background(), f.actor, HostRequest{RunID: run.ID, HostID: f.hosts[1].ID}); err != nil {
				t.Fatal(err)
			}
			if !stop {
				current, err := f.db.Run(context.Background(), f.actor, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.Hosts[0].FinishedAt != 0 {
					t.Fatal("queued cancellation stopped running host under continue policy")
				}
				release <- struct{}{}
				select {
				case id := <-started:
					if id != f.hosts[2].ID {
						t.Fatal("cancelled queued host ran")
					}
				case <-time.After(time.Second):
					t.Fatal("remaining host did not run")
				}
				release <- struct{}{}
			}
			result := finished(t, f, run.ID)
			if stop && result.Hosts[0].Status != "cancelled" {
				t.Fatal("queued cancellation did not stop the active host")
			}
			if result.Hosts[1].Status != "cancelled" {
				t.Fatal("queued cancellation not persisted")
			}
		})
	}
}

func TestBrowserRequestCancellationDoesNotStopAdmittedRun(t *testing.T) {
	f := newFixture(t, "sequential", false)
	ctx := context.Background()
	job, err := jobService(t, f.db, f.vault).Save(ctx, f.actor, f.job.ID, jobs.Input{Name: f.job.Name, Script: f.job.Script, HostIDs: []string{f.hosts[0].ID}, ExecutionMode: "sequential", Version: f.job.Version})
	if err != nil {
		t.Fatal(err)
	}
	f.job = job
	ready := make(chan struct{})
	writing := make(chan struct{})
	allowRead := make(chan struct{})
	f.engine.runner = func(ctx context.Context, _ store.Connection, _ string, output io.Writer, attach func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		stop := context.AfterFunc(ctx, func() { reader.Close() })
		defer stop()
		attach(&notifyingInput{WriteCloser: writer, started: writing})
		if _, err := io.WriteString(output, "Before browser closed\n"); err != nil {
			return nil, err
		}
		close(ready)
		select {
		case <-allowRead:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		input := make([]byte, 32)
		count, err := reader.Read(input)
		if err != nil {
			return nil, err
		}
		if _, err := output.Write(input[:count]); err != nil {
			return nil, err
		}
		code := 0
		return &code, nil
	}
	requestCtx, closeBrowser := context.WithCancel(ctx)
	run, err := f.engine.Start(requestCtx, f.actor, StartRequest{JobID: f.job.ID, Version: f.job.Version, RequestID: "fixture-intent"})
	if err != nil {
		t.Fatal(err)
	}
	<-ready
	closeBrowser()
	restored, err := f.db.Run(ctx, f.actor, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != "running" || restored.Hosts[0].Status != "running" {
		t.Fatal("browser close stopped the run")
	}
	inputCtx, closeInputPage := context.WithCancel(ctx)
	inputResult := make(chan error, 1)
	go func() {
		inputResult <- f.engine.Input(inputCtx, f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "Input after revisit\n"})
	}()
	<-writing
	closeInputPage()
	close(allowRead)
	if err := <-inputResult; err != nil {
		t.Fatal(err)
	}
	if completed := finished(t, f, run.ID); completed.Status != "succeeded" {
		t.Fatal(completed)
	}
	chunks, err := f.db.RunOutput(ctx, f.actor, run.ID, f.hosts[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for _, chunk := range chunks {
		output.Write(chunk.Data)
	}
	if output.String() != "Before browser closed\nInput after revisit\n" {
		t.Fatal("history was not preserved across browser revisit")
	}
}

type notifyingInput struct {
	io.WriteCloser
	started chan struct{}
}

func (w *notifyingInput) Write(data []byte) (int, error) {
	close(w.started)
	return w.WriteCloser.Write(data)
}

func TestSudoPolicyRequiresJobAndHostAndIsSnapshotted(t *testing.T) {
	for _, jobSudo := range []bool{false, true} {
		t.Run(fmt.Sprint(jobSudo), func(t *testing.T) {
			f := newFixture(t, "sequential", true)
			ctx := context.Background()
			hostService, err := hosts.New(ctx, f.db, f.vault)
			if err != nil {
				t.Fatal(err)
			}
			for index, host := range f.hosts {
				username := "fixture"
				if index == 2 {
					username = "root"
				}
				updated, err := hostService.Save(ctx, f.actor, host.ID, hosts.Input{Name: host.Name, Address: host.Address, Port: host.Port, Username: username, RunAsSudo: index != 1, Version: host.Version})
				if err != nil {
					t.Fatal(err)
				}
				f.hosts[index] = updated
			}
			f.job, err = jobService(t, f.db, f.vault).Save(ctx, f.actor, f.job.ID, jobs.Input{Name: f.job.Name, Script: f.job.Script, HostIDs: []string{f.hosts[0].ID, f.hosts[1].ID, f.hosts[2].ID}, ExecutionMode: "sequential", RunAsSudo: jobSudo, Version: f.job.Version})
			if err != nil {
				t.Fatal(err)
			}
			runAdmission, err := f.db.AdmitRun(ctx, f.actor, store.RunAdmission{ID: "sudo-policy", RequestID: "sudo-policy", JobID: f.job.ID, Version: f.job.Version})
			run := runAdmission.PublicRun()
			snapshot := runAdmission.(store.CreatedRun).Job
			connections := runAdmission.(store.CreatedRun).Connections
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.RunAsSudo != jobSudo || run.RunAsSudo != jobSudo {
				t.Fatal("job sudo not snapshotted")
			}
			for index, c := range connections {
				if c.RunAsSudo != (jobSudo && index == 0) {
					t.Fatalf("wrong elevation for host %d: %+v", index, c.Host)
				}
			}
			_, err = jobService(t, f.db, f.vault).Save(ctx, f.actor, f.job.ID, jobs.Input{Name: f.job.Name, Script: f.job.Script, HostIDs: []string{f.hosts[0].ID}, ExecutionMode: "sequential", RunAsSudo: !jobSudo, Version: f.job.Version})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := f.db.Run(ctx, f.actor, run.ID)
			if err != nil || stored.RunAsSudo != jobSudo {
				t.Fatalf("run sudo changed after job edit: %+v %v", stored, err)
			}
			listed, err := f.db.Runs(ctx, f.actor, f.job.ID)
			if err != nil || len(listed) != 1 || listed[0].RunAsSudo != jobSudo {
				t.Fatalf("run history sudo missing: %+v %v", listed, err)
			}
		})
	}
}

func jobService(t *testing.T, db *store.Store, vault *secrets.Vault) *jobs.Service {
	t.Helper()
	service, err := jobs.New(context.Background(), db, vault)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
