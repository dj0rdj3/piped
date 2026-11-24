package store

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestMissingJobHostsRollBackCreationEditsAndCopies(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, db, user, "session")
	for _, id := range []string{"first", "second"} {
		_, err := db.SaveHost(ctx, actor, Host{
			ID: id, Name: id, Address: "localhost", Port: 22, Username: "fixture",
		}, []byte("encrypted password"), nil, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	original, err := db.SaveJob(ctx, actor, Job{
		ID: "original", Name: "Original", Script: "echo original",
		HostIDs: []string{"second", "first"}, ExecutionMode: "sequential",
		Schedule: &JobSchedule{Cron: "0 0 * * *"},
	}, []JobSecret{{Name: "TOKEN", Ciphertext: []byte("encrypted job secret")}}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"create", "edit", "copy"} {
		t.Run(mode, func(t *testing.T) {
			candidate := original
			candidate.Name, candidate.Script = mode, "echo changed"
			candidate.HostIDs = []string{"first", "missing", "second"}
			candidate.ExecutionMode, candidate.StopOnFailure = "parallel", true
			candidate.Schedule = &JobSchedule{Cron: "* * * * *", Enabled: true}
			if mode != "edit" {
				candidate.ID, candidate.Version = mode, 0
			}
			changes := []JobSecret{{Name: "REPLACEMENT", Ciphertext: []byte("replacement")}}
			if mode == "copy" {
				_, err = db.CopyJob(ctx, actor, candidate, changes, JobCopy{ID: original.ID, Version: original.Version},
					func(secrets []JobSecret) ([]JobSecret, error) { return secrets, nil })
			} else {
				_, err = db.SaveJob(ctx, actor, candidate, changes, mode == "create")
			}
			if !errors.Is(err, ErrJobHost) {
				t.Fatalf("missing target did not return the public host error: %v", err)
			}
			jobs, err := db.Jobs(ctx, actor)
			if err != nil || len(jobs) != 1 || !reflect.DeepEqual(jobs[0], original) {
				t.Fatalf("failed %s changed the definition, order, schedule, or version: %+v %v", mode, jobs, err)
			}
		})
	}
	_, _, connections, err := db.CreateRun(ctx, actor, "snapshot", original.ID, original.Version)
	if err != nil || len(connections) != 2 || len(connections[0].JobSecrets) != 1 {
		t.Fatalf("original job was not executable after rejected writes: %v", err)
	}
	secret := connections[0].JobSecrets[0]
	if secret.Name != "TOKEN" || !bytes.Equal(secret.Ciphertext, []byte("encrypted job secret")) {
		t.Fatal("a rejected mutation replaced the original secret")
	}
}

func TestJobScheduleWatermarkAndSecretRollback(t *testing.T) {
	db, actor, job := scheduledJobFixture(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `UPDATE jobs SET schedule_after = 123, last_schedule_minute = 456 WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	for _, omit := range []bool{true, false} {
		if omit {
			job.Schedule = nil
		}
		var err error
		job, err = db.SaveJob(ctx, actor, job, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		var after, last int64
		if err := db.db.QueryRowContext(ctx, `SELECT schedule_after, last_schedule_minute FROM jobs WHERE id = ?`, job.ID).Scan(&after, &last); err != nil || after != 123 || last != 456 {
			t.Fatalf("unchanged schedule reset its watermarks: %d, %d, %v", after, last, err)
		}
	}
	candidate := job
	candidate.Schedule = &JobSchedule{Cron: "*/5 * * * *"}
	// The first secret is inserted before the second fails. The entire edit,
	// including its version and schedule watermark, must roll back.
	changes := []JobSecret{{Name: "NEW", Ciphertext: []byte("replacement")}, {Name: "MISSING"}}
	if _, err := db.SaveJob(ctx, actor, candidate, changes, false); !errors.Is(err, ErrJobSecret) {
		t.Fatalf("missing preserved value accepted: %v", err)
	}
	listed, err := db.Jobs(ctx, actor)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], job) {
		t.Fatalf("failed secret edit changed the job: %+v, %v", listed, err)
	}
	var after, last int64
	if err := db.db.QueryRowContext(ctx, `SELECT schedule_after, last_schedule_minute FROM jobs WHERE id = ?`, job.ID).Scan(&after, &last); err != nil || after != 123 || last != 456 {
		t.Fatalf("failed secret edit reset watermarks: %d, %d, %v", after, last, err)
	}
	before := time.Now().Unix()
	changes = []JobSecret{{Name: "Z_LAST", Ciphertext: []byte("new value")}, {Name: "TOKEN"}, {Name: "A_FIRST", Ciphertext: []byte("another value")}, {Name: "_UNDERSCORE", Ciphertext: []byte("underscore value")}}
	updated, err := db.SaveJob(ctx, actor, candidate, changes, false)
	if err != nil || len(updated.SecretNames) != len(changes) {
		t.Fatalf("secret metadata is incomplete: %+v, %v", updated.SecretNames, err)
	}
	listed, err = db.Jobs(ctx, actor)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], updated) {
		t.Fatalf("saved metadata differs from listing order: %+v, %v", listed, err)
	}
	if changes[1].Ciphertext != nil {
		t.Fatal("saving mutated the caller's secret changes")
	}
	if err := db.db.QueryRowContext(ctx, `SELECT schedule_after, last_schedule_minute FROM jobs WHERE id = ?`, job.ID).Scan(&after, &last); err != nil || after < before || after > time.Now().Unix() || last != 456 {
		t.Fatalf("changed schedule did not reset only its activation time: %d, %d, %v", after, last, err)
	}
}
