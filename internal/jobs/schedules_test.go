package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"piped/internal/auth"
	"piped/internal/store"
)

func TestScheduleValidationAndPreservation(t *testing.T) {
	service, _, actor, targets := jobHarness(t)
	input := jobInput(targets)
	for _, schedule := range []store.JobSchedule{
		{Enabled: true}, {Cron: "60 * * * *"}, {Cron: "@daily", Enabled: true},
		{Cron: "* * * * *\n"}, {Cron: "*\u00a0* * * *"}, {Cron: strings.Repeat(" ", 257)},
	} {
		input.Schedule = &schedule
		if _, err := validate(input); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid schedule accepted: %+v: %v", schedule, err)
		}
	}
	input.Schedule = &store.JobSchedule{Cron: "  */5\t * * * MON-FRI  ", Enabled: true}
	job, err := service.Save(context.Background(), actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	if job.Schedule.Cron != "*/5 * * * MON-FRI" || !job.Schedule.Enabled {
		t.Fatalf("schedule not normalized: %+v", job.Schedule)
	}
	input.Version, input.Schedule = job.Version, nil
	job, err = service.Save(context.Background(), actor, job.ID, input)
	if err != nil || !job.Schedule.Enabled {
		t.Fatalf("omitted schedule not preserved: %v", err)
	}
	input.Version, input.Schedule = job.Version, &store.JobSchedule{Cron: job.Schedule.Cron}
	job, err = service.Save(context.Background(), actor, job.ID, input)
	if err != nil || job.Schedule.Enabled || job.Schedule.Cron == "" {
		t.Fatalf("pause lost expression: %v", err)
	}
}

func TestEnabledScheduleRequiresExecutionAuthority(t *testing.T) {
	service, db, actor, targets := jobHarness(t)
	ctx := context.Background()
	key, token, err := auth.NewAPIKey(actor.User, "Jobs editor", []string{store.ScopeJobsWrite}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateLocalAPIKey(ctx, actor.User, key, auth.TokenHash(token)); err != nil {
		t.Fatal(err)
	}
	editor, err := db.ResolveAPIKey(ctx, auth.TokenHash(token), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	input := jobInput(targets)
	input.Schedule = &store.JobSchedule{Cron: "* * * * *", Enabled: true}
	if _, err := service.Save(ctx, editor, "", input); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("enabled without runs:write: %v", err)
	}
	job, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Version, input.Schedule = job.Version, nil
	input.Script = "echo unauthorized unattended change\n"
	if _, err := service.Save(ctx, editor, job.ID, input); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("enabled edit without runs:write: %v", err)
	}
	input.Schedule = &store.JobSchedule{Cron: job.Schedule.Cron}
	if _, err := service.Save(ctx, editor, job.ID, input); err != nil {
		t.Fatalf("cannot disable schedule: %v", err)
	}
}
