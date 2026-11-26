package execution

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/jobs"
	"piped/internal/store"
	"piped/internal/testdatabase"
)

func TestAdmissionCommitDisconnectDoesNotStrandOrReplayRuns(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		for _, afterCommit := range []bool{false, true} {
			name := map[bool]string{false: "manual", true: "scheduled"}[scheduled] + "/" + map[bool]string{false: "rolled_back", true: "durably_committed"}[afterCommit]
			t.Run(name, func(t *testing.T) {
				driver, dsn := testdatabase.Settings(t)
				if driver == "sqlite" {
					t.Skip("requires a disposable server wire protocol")
				}
				proxyDSN, dropped := testdatabase.DropAdmissionCommit(t, driver, dsn, afterCommit)
				f := newFixtureWithDatabase(t, "parallel", false, store.Config{Driver: driver, DSN: proxyDSN})
				job, err := jobService(t, f.db, f.vault).Save(context.Background(), f.actor, f.job.ID, jobs.Input{
					Name: f.job.Name, Script: f.job.Script, HostIDs: f.job.HostIDs,
					ExecutionMode: "parallel", Version: f.job.Version,
					Schedule: &store.JobSchedule{Cron: "* * * * *", Enabled: true},
				})
				if err != nil {
					t.Fatal(err)
				}
				f.job = job
				var clock atomic.Int64
				at := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
				clock.Store(at.Unix())
				f.engine.mu.Lock()
				f.engine.now = func() time.Time { return time.Unix(clock.Load(), 0) }
				f.engine.mu.Unlock()
				var calls atomic.Int32
				f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
					calls.Add(1)
					code := 0
					return &code, nil
				}
				if scheduled {
					f.engine.runSchedules(at)
				} else if _, err := f.engine.Start(context.Background(), f.actor, StartRequest{JobID: job.ID, Version: job.Version, RequestID: "faulted-admission"}); err == nil {
					t.Fatal("lost commit acknowledgement was reported as successful admission")
				}
				select {
				case <-dropped:
				case <-time.After(time.Second):
					t.Fatal("did not drop the exact admission COMMIT")
				}
				await(t, func() bool {
					f.engine.mu.Lock()
					defer f.engine.mu.Unlock()
					return len(f.engine.active) == 0
				})
				runs := schedulerRuns(t, f)
				if afterCommit {
					if len(runs) != 1 || runs[0].Status != "failed" || runs[0].FinishedAt == 0 {
						t.Fatal("durably admitted run was stranded after lost COMMIT reply", runs)
					}
					current := finished(t, f, runs[0].ID)
					for _, host := range current.Hosts {
						if host.Status != "failed" || host.FinishedAt == 0 || !strings.Contains(host.Message, "script was not started") {
							t.Fatal("unlaunched host was not reconciled", host)
						}
					}
				} else if len(runs) != 0 {
					t.Fatal("rolled back admission created history", runs)
				}
				if calls.Load() != 0 {
					t.Fatal("ambiguous admission executed or replayed SSH", calls.Load())
				}
				if afterCommit && !scheduled {
					replayed, err := f.engine.Start(context.Background(), f.actor, StartRequest{JobID: job.ID, Version: job.Version, RequestID: "faulted-admission"})
					if err != nil || replayed.ID != runs[0].ID || replayed.Status != "failed" || calls.Load() != 0 {
						t.Fatal("retry after a lost successful COMMIT launched scripts", replayed.ID, err, calls.Load())
					}
				}
				// A later cron occurrence must run normally, proving reconciliation
				// removed the unfinished-row overlap block without restarting.
				clock.Store(at.Add(time.Minute).Unix())
				f.engine.runSchedules(at.Add(time.Minute))
				runs = schedulerRuns(t, f)
				expectedRuns := 1
				if afterCommit {
					expectedRuns++
				}
				if len(runs) != expectedRuns {
					t.Fatal("later occurrence was blocked by ambiguous admission", runs)
				}
				if current := finished(t, f, runs[0].ID); current.Status != "succeeded" || calls.Load() != int32(len(f.hosts)) {
					t.Fatal("later occurrence did not execute exactly once", current, calls.Load())
				}
			})
		}
	}
}
