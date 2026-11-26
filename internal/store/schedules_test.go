package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"piped/internal/cron"
)

func scheduledJobFixture(t *testing.T) (*Store, Actor, Job) {
	t.Helper()
	db := openTest(t)
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, db, user, "session")
	if _, err := db.SaveHost(ctx, actor, Host{ID: "host", Name: "Host", Address: "localhost", Port: 22, Username: "fixture"}, []byte("password"), nil, true); err != nil {
		t.Fatal(err)
	}
	job, err := db.SaveJob(ctx, actor, Job{
		ID: "job", Name: "Scheduled job", Script: "echo fixture", HostIDs: []string{"host"}, ExecutionMode: "sequential",
		Schedule: &JobSchedule{Cron: "* * * * *", Enabled: true},
	}, []JobSecret{{Name: "TOKEN", Ciphertext: []byte("encrypted value")}}, true)
	if err != nil {
		t.Fatal(err)
	}
	return db, actor, job
}

func TestScheduledAdmissionEligibilityAndWatermark(t *testing.T) {
	db, actor, job := scheduledJobFixture(t)
	ctx := context.Background()
	at := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	minute := at.Unix()
	clock := func() time.Time { return at }
	notDue := fmt.Sprintf("%d * * * *", at.Add(time.Minute).In(time.Local).Minute())
	for _, fixture := range []struct {
		name, jobID, expression           string
		version, slot, enabledAfter, last int64
		enabled                           bool
		want                              error
	}{
		{"missing", "missing", "* * * * *", job.Version, minute, 0, 0, true, ErrScheduleSkipped},
		{"stale version", job.ID, "* * * * *", job.Version + 1, minute, 0, 0, true, ErrScheduleSkipped},
		{"paused", job.ID, "* * * * *", job.Version, minute, 0, 0, false, ErrScheduleSkipped},
		{"not due", job.ID, notDue, job.Version, minute, 0, 0, true, ErrScheduleSkipped},
		{"enabled in this slot", job.ID, "* * * * *", job.Version, minute, minute, 0, true, ErrScheduleSkipped},
		{"enabled after this slot", job.ID, "* * * * *", job.Version, minute, minute + 60, 0, true, ErrScheduleSkipped},
		{"already observed", job.ID, "* * * * *", job.Version, minute, 0, minute, true, ErrScheduleSkipped},
		{"clock moved backward", job.ID, "* * * * *", job.Version, minute, 0, minute + 60, true, ErrScheduleSkipped},
		{"unaligned", job.ID, "* * * * *", job.Version, minute + 1, 0, 0, true, ErrScheduleSkipped},
		{"missed", job.ID, "* * * * *", job.Version, minute - 60, 0, 0, true, ErrScheduleSkipped},
		{"future", job.ID, "* * * * *", job.Version, minute + 60, 0, 0, true, ErrScheduleSkipped},
		{"invalid stored cron", job.ID, "damaged", job.Version, minute, 0, 0, true, cron.ErrSyntax},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			_, err := db.db.ExecContext(ctx, `UPDATE jobs SET schedule_cron = ?, schedule_enabled = ?, schedule_after = ?, last_schedule_minute = ? WHERE id = ?`,
				fixture.expression, fixture.enabled, fixture.enabledAfter, fixture.last, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.CreateScheduledRun(ctx, "rejected", fixture.jobID, fixture.version, fixture.slot, clock)
			if !errors.Is(err, fixture.want) {
				t.Fatalf("got %v, want %v", err, fixture.want)
			}
			var last int64
			if err := db.db.QueryRowContext(ctx, `SELECT last_schedule_minute FROM jobs WHERE id = ?`, job.ID).Scan(&last); err != nil || last != fixture.last {
				t.Fatalf("rejected admission consumed a slot: %d, %v", last, err)
			}
			runs, err := db.Runs(ctx, actor, job.ID)
			if err != nil || len(runs) != 0 {
				t.Fatalf("rejected admission persisted a run: %+v, %v", runs, err)
			}
		})
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE jobs SET schedule_cron = ?, schedule_after = ?, last_schedule_minute = ? WHERE id = ?`, "* * * * *", minute-1, minute-60, job.ID); err != nil {
		t.Fatal(err)
	}
	created, err := db.CreateScheduledRun(ctx, "admitted", job.ID, job.Version, minute, clock)
	run, snapshot, connections := created.Run, created.Job, created.Connections
	if err != nil || run.Trigger != "schedule" || run.ScheduledAt != minute || snapshot.Version != job.Version || len(connections) != 1 || len(connections[0].JobSecrets) != 1 {
		t.Fatalf("eligible occurrence did not snapshot the job: %+v, %v", run, err)
	}
	if err := db.FinishRun(ctx, run.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateScheduledRun(ctx, "duplicate", job.ID, job.Version, minute, clock); !errors.Is(err, ErrScheduleSkipped) {
		t.Fatalf("completed occurrence admitted twice: %v", err)
	}
}

func TestScheduledSnapshotFailureDoesNotConsumeOccurrence(t *testing.T) {
	db, actor, job := scheduledJobFixture(t)
	ctx := context.Background()
	at := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	clock := func() time.Time { return at }
	if _, err := db.AdmitRun(ctx, actor, RunAdmission{ID: "existing", RequestID: "existing", JobID: job.ID, Version: job.Version}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, "existing", "succeeded"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateScheduledRun(ctx, "existing", job.ID, job.Version, at.Unix(), clock); err == nil || errors.Is(err, ErrScheduleSkipped) {
		t.Fatalf("snapshot insertion failure not reported: %v", err)
	}
	if _, err := db.CreateScheduledRun(ctx, "retry", job.ID, job.Version, at.Unix(), clock); err != nil {
		t.Fatalf("failed snapshot consumed occurrence: %v", err)
	}
}
