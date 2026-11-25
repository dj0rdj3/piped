package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"piped/internal/auth"
	"piped/internal/store"
)

func TestCopyJobSecretsAndIndependentUpdates(t *testing.T) {
	service, db, actor, targets := jobHarness(t)
	ctx := context.Background()
	input := jobInput(targets)
	input.HostTags = []string{"production"}
	input.HostIDs = []string{targets[0].ID}
	input.ExcludedHostIDs = []string{targets[1].ID}
	input.Secrets = []SecretInput{{Name: "TOKEN", Value: secretValue("source-secret")}, {Name: "REMOVE_ME", Value: secretValue("removed-secret")}}
	input.Schedule = &store.JobSchedule{Cron: "0 2 * * *", Enabled: true}
	source, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Name = "Copied job"
	input.CopyFrom = &store.JobCopy{ID: source.ID, Version: source.Version}
	input.Schedule = &store.JobSchedule{Cron: source.Schedule.Cron}
	input.Secrets = []SecretInput{{Name: "TOKEN"}, {Name: "NEW_TOKEN", Value: secretValue("new-secret")}}
	copied, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(copied.HostTags, source.HostTags) {
		t.Fatal("copied job lost its dynamic tag targets")
	}
	if !slices.Equal(copied.ExcludedHostIDs, source.ExcludedHostIDs) {
		t.Fatal("copied job lost its host exclusions")
	}
	if copied.ID == source.ID || copied.Version != 1 || copied.Schedule.Enabled || !slices.Equal(copied.HostIDs, source.HostIDs) || copied.Script != source.Script || !slices.Equal(copied.SecretNames, []string{"NEW_TOKEN", "TOKEN"}) {
		t.Fatalf("incorrect copy: %+v", copied)
	}
	_, _, connections, err := db.CreateRun(ctx, actor, "copy-snapshot", copied.ID, copied.Version)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range connections[0].JobSecrets {
		plain, err := service.vault.Open("job:"+copied.ID+":env:"+secret.Name, secret.Ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		want := "source-secret"
		if secret.Name == "NEW_TOKEN" {
			want = "new-secret"
		}
		if string(plain) != want {
			t.Fatal("copied value changed")
		}
		clear(plain)
		if _, err := service.vault.Open("job:"+source.ID+":env:"+secret.Name, secret.Ciphertext); err == nil {
			t.Fatal("ciphertext not rebound to new job")
		}
	}
	// Omitting the secret list copies all values; an empty list deliberately copies none.
	input.Name = "All secrets"
	input.Secrets = nil
	input.Schedule = nil
	all, err := service.Save(ctx, actor, "", input)
	if err != nil || !slices.Equal(all.SecretNames, source.SecretNames) || all.Schedule.Enabled {
		t.Fatalf("copy all: %+v %v", all, err)
	}
	input.Name = "No secrets"
	input.Secrets = []SecretInput{}
	none, err := service.Save(ctx, actor, "", input)
	if err != nil || len(none.SecretNames) != 0 {
		t.Fatalf("copy none: %+v %v", none, err)
	}
	// Editing the copy never changes the source or a previously admitted run.
	input.CopyFrom = nil
	input.Version = copied.Version
	input.Name = copied.Name
	input.Secrets = []SecretInput{{Name: "TOKEN", Value: secretValue("replacement")}}
	if _, err := service.Save(ctx, actor, copied.ID, input); err != nil {
		t.Fatal(err)
	}
	_, _, original, err := db.CreateRun(ctx, actor, "source-snapshot", source.ID, source.Version)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range original[0].JobSecrets {
		if secret.Name == "TOKEN" {
			plain, err := service.vault.Open("job:"+source.ID+":env:TOKEN", secret.Ciphertext)
			if err != nil || string(plain) != "source-secret" {
				t.Fatal("source value changed")
			}
			clear(plain)
		}
	}
	listed, err := service.List(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(listed)
	for _, sensitive := range []string{"source-secret", "removed-secret", "new-secret", "replacement", "ciphertext"} {
		if bytes.Contains(encoded, []byte(sensitive)) {
			t.Fatal("copy exposed secret material")
		}
	}
}

func TestCopyJobVersionAuthorizationAndRollback(t *testing.T) {
	service, db, actor, targets := jobHarness(t)
	ctx := context.Background()
	input := jobInput(targets)
	input.Secrets = []SecretInput{{Name: "TOKEN", Value: secretValue("fixture-secret")}}
	source, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Name = "Duplicate"
	input.Secrets = nil
	input.CopyFrom = &store.JobCopy{ID: source.ID, Version: source.Version + 1}
	if _, err := service.Save(ctx, actor, "", input); !errors.Is(err, store.ErrJobChanged) {
		t.Fatalf("stale copy: %v", err)
	}
	input.CopyFrom.Version = source.Version
	input.Secrets = []SecretInput{{Name: "MISSING"}}
	if _, err := service.Save(ctx, actor, "", input); !errors.Is(err, store.ErrJobSecret) {
		t.Fatalf("unknown source secret: %v", err)
	}
	input.Secrets = nil
	key, token, err := auth.NewAPIKey(actor.User, "writer", []string{store.ScopeJobsWrite}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateLocalAPIKey(ctx, actor.User, key, auth.TokenHash(token)); err != nil {
		t.Fatal(err)
	}
	writer, err := db.ResolveAPIKey(ctx, auth.TokenHash(token), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Save(ctx, writer, "", input); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("copy without read scope: %v", err)
	}
	if err := db.RevokeLocalAPIKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Save(ctx, writer, "", input); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("revoked copy: %v", err)
	}
	if _, err := service.Save(ctx, actor, source.ID, input); !errors.Is(err, ErrInput) {
		t.Fatalf("copy on update: %v", err)
	}
	// A corrupt source must roll back without a partial duplicate.
	if _, err := db.SaveJob(ctx, actor, source, []store.JobSecret{{Name: "TOKEN", Ciphertext: []byte("damaged")}}, false); err != nil {
		t.Fatal(err)
	}
	input.CopyFrom.Version++
	if _, err := service.Save(ctx, actor, "", input); err == nil {
		t.Fatal("copied damaged ciphertext")
	}
	listed, err := service.List(ctx, actor)
	if err != nil || len(listed) != 1 {
		t.Fatalf("partial copy persisted: %v", err)
	}
}

func TestLatestRunSummaryAndReadScope(t *testing.T) {
	service, db, actor, targets := jobHarness(t)
	ctx := context.Background()
	job, err := service.Save(ctx, actor, "", jobInput(targets))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(ctx, actor)
	if err != nil || listed[0].LatestRun != nil {
		t.Fatal("new job has run")
	}
	for _, id := range []string{"first", "second"} {
		if _, _, _, err := db.CreateRun(ctx, actor, id, job.ID, job.Version); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.FinishRun(ctx, "second", "interrupted"); err != nil {
		t.Fatal(err)
	}
	listed, err = service.List(ctx, actor)
	if err != nil || listed[0].LatestRun == nil || listed[0].LatestRun.ID != "second" || listed[0].LatestRun.Status != "interrupted" {
		t.Fatalf("latest run incorrect: %+v %v", listed, err)
	}
	key, token, err := auth.NewAPIKey(actor.User, "jobs reader", []string{store.ScopeJobsRead}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateLocalAPIKey(ctx, actor.User, key, auth.TokenHash(token)); err != nil {
		t.Fatal(err)
	}
	reader, err := db.ResolveAPIKey(ctx, auth.TokenHash(token), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	listed, err = service.List(ctx, reader)
	if err != nil || listed[0].LatestRun != nil {
		t.Fatal("run summary exposed without runs:read")
	}
	if err := db.DeleteCompletedRun(ctx, actor, "second"); err != nil {
		t.Fatal(err)
	}
	listed, err = service.List(ctx, actor)
	if err != nil || listed[0].LatestRun.ID != "first" {
		t.Fatalf("did not fall back to retained run: %v", err)
	}
}
