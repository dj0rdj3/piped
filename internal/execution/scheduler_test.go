package execution

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/jobs"
	"piped/internal/store"
)

func scheduledFixture(t *testing.T, expression string) (*engineFixture, *atomic.Int64, time.Time) {
	t.Helper()
	f := newFixture(t, "sequential", true)
	var err error
	f.job, err = jobService(t, f.db, f.vault).Save(context.Background(), f.actor, f.job.ID, jobs.Input{
		Name: f.job.Name, Script: f.job.Script, HostIDs: []string{f.hosts[0].ID}, ExecutionMode: "sequential", Version: f.job.Version,
		Schedule: &store.JobSchedule{Cron: expression, Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	clock := &atomic.Int64{}
	clock.Store(at.Unix())
	f.engine.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	f.engine.scheduleMinute = at.Add(-time.Minute).Unix()
	return f, clock, at
}

func schedulerRuns(t *testing.T, f *engineFixture) []store.Run {
	t.Helper()
	runs, err := f.db.Runs(context.Background(), f.actor, f.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

func TestSchedulerSkipsOverlapWithoutQueuingOrRetrying(t *testing.T) {
	f, clock, at := scheduledFixture(t, "* * * * *")
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	defer close(release)
	f.engine.runner = func(ctx context.Context, _ store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		code := 0
		return &code, nil
	}
	f.engine.runSchedules(at)
	<-started
	runs := schedulerRuns(t, f)
	if len(runs) != 1 || runs[0].Trigger != "schedule" || runs[0].ScheduledAt != at.Unix() {
		t.Fatalf("scheduled provenance missing: %+v", runs)
	}
	first := runs[0].ID
	for index := 1; index <= 3; index++ {
		next := at.Add(time.Duration(index) * time.Minute)
		clock.Store(next.Unix())
		f.engine.runSchedules(next)
	}
	if len(schedulerRuns(t, f)) != 1 {
		t.Fatal("overlapping schedule admitted")
	}
	release <- struct{}{}
	finished(t, f, first)
	current := at.Add(3 * time.Minute)
	f.engine.runSchedules(current)
	if len(schedulerRuns(t, f)) != 1 {
		t.Fatal("skipped occurrence queued or retried in same minute")
	}
	next := at.Add(4 * time.Minute)
	clock.Store(next.Unix())
	f.engine.runSchedules(next)
	<-started
	runs = schedulerRuns(t, f)
	if len(runs) != 2 {
		t.Fatal("later occurrence not admitted")
	}
	release <- struct{}{}
	finished(t, f, runs[0].ID)
}

func TestScheduledOccurrenceSkipsManualAndConcurrentAdmissions(t *testing.T) {
	f, _, at := scheduledFixture(t, "* * * * *")
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{}, 1)
	f.engine.runner = func(ctx context.Context, _ store.Connection, _ string, _ io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		code := 0
		return &code, nil
	}
	manual := startFixture(t, f)
	<-started
	var group sync.WaitGroup
	for range 5 {
		group.Go(func() { f.engine.runSchedules(at) })
	}
	group.Wait()
	runs := schedulerRuns(t, f)
	if len(runs) != 1 || runs[0].Trigger != "manual" {
		t.Fatal("cron overlapped a manual run")
	}
	release <- struct{}{}
	finished(t, f, manual.ID)
	// The database watermark independently prevents duplicates in this minute.
	_, _, _, err := f.db.CreateScheduledRun(context.Background(), "duplicate", f.job.ID, f.job.Version, at.Unix(), f.engine.now)
	if err != store.ErrScheduleSkipped {
		t.Fatalf("skipped occurrence retried after manual completion: %v", err)
	}
}

func TestSchedulerDoesNotCatchUpAfterDelayOrRestart(t *testing.T) {
	f, clock, at := scheduledFixture(t, "* * * * *")
	f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
		code := 0
		return &code, nil
	}
	jumped := at.Add(30 * time.Minute)
	clock.Store(jumped.Unix())
	f.engine.runSchedules(jumped)
	runs := schedulerRuns(t, f)
	if len(runs) != 1 || runs[0].ScheduledAt != jumped.Unix() {
		t.Fatal("missed minutes were replayed")
	}
	finished(t, f, runs[0].ID)
	f.engine.Close()
	restarted, err := New(context.Background(), f.db, f.vault, nil, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartAt := jumped.Add(40 * time.Minute)
	restarted.now = func() time.Time { return restartAt }
	restarted.StartScheduler()
	restarted.StartScheduler()
	if len(schedulerRuns(t, f)) != 1 || restarted.scheduleMinute != restartAt.Unix() {
		t.Fatal("restart caught up or failed to establish its startup watermark")
	}
	restarted.runSchedules(restartAt)
	if len(schedulerRuns(t, f)) != 1 {
		t.Fatal("startup minute ran retroactively")
	}
}

func TestSchedulerRevalidatesCurrentMinuteAndSchedule(t *testing.T) {
	f, clock, at := scheduledFixture(t, "* * * * *")
	clock.Store(at.Add(time.Minute).Unix())
	_, _, _, err := f.db.CreateScheduledRun(context.Background(), "late", f.job.ID, f.job.Version, at.Unix(), f.engine.now)
	if err != store.ErrScheduleSkipped {
		t.Fatal("late admission caught up a missed minute")
	}
	clock.Store(at.Unix())
	_, err = jobService(t, f.db, f.vault).Save(context.Background(), f.actor, f.job.ID, jobs.Input{Name: f.job.Name, Script: f.job.Script, HostIDs: []string{f.hosts[0].ID}, ExecutionMode: "sequential", Version: f.job.Version, Schedule: &store.JobSchedule{Cron: "* * * * *", Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = f.db.CreateScheduledRun(context.Background(), "disabled", f.job.ID, f.job.Version, at.Unix(), f.engine.now)
	if err != store.ErrScheduleSkipped || len(schedulerRuns(t, f)) != 0 {
		t.Fatal("stale enabled schedule admitted")
	}
	f.engine.Close()
	f.engine.runSchedules(at)
	if len(schedulerRuns(t, f)) != 0 {
		t.Fatal("closed scheduler admitted work")
	}
}

func TestScheduledAdmissionIsAtomicAndNeverStartsLate(t *testing.T) {
	f, _, at := scheduledFixture(t, "* * * * *")
	calls := 0
	changingClock := func() time.Time {
		calls++
		if calls == 1 {
			return at
		}
		return at.Add(time.Minute)
	}
	_, _, _, err := f.db.CreateScheduledRun(context.Background(), "late-snapshot", f.job.ID, f.job.Version, at.Unix(), changingClock)
	if err != store.ErrScheduleSkipped || len(schedulerRuns(t, f)) != 0 {
		t.Fatal("snapshot started after its scheduled minute")
	}
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"concurrent-one", "concurrent-two"} {
		group.Go(func() {
			_, _, _, err := f.db.CreateScheduledRun(context.Background(), id, f.job.ID, f.job.Version, at.Unix(), f.engine.now)
			results <- err
		})
	}
	group.Wait()
	close(results)
	admitted, skipped := 0, 0
	for err := range results {
		if err == nil {
			admitted++
		} else if err == store.ErrScheduleSkipped {
			skipped++
		} else {
			t.Fatal(err)
		}
	}
	if admitted != 1 || skipped != 1 || len(schedulerRuns(t, f)) != 1 {
		t.Fatal("concurrent schedule admissions were not atomic")
	}
}
