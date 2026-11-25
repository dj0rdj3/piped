package jobs

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"piped/internal/auth"
	"piped/internal/hosts"
	"piped/internal/secrets"
	"piped/internal/store"
)

func jobHarness(t *testing.T) (*Service, *store.Store, store.Actor, []store.Host) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "data", "piped.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := db.InitAdmin(ctx, "admin", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, user, "session", time.Now().Unix(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{User: user, SessionHash: "session"}
	vault, err := secrets.New(bytes.Repeat([]byte{27}, 32))
	if err != nil {
		t.Fatal(err)
	}
	hostService, err := hosts.New(ctx, db, vault)
	if err != nil {
		t.Fatal(err)
	}
	targets := []store.Host{}
	for _, name := range []string{"Alpha", "Beta"} {
		password := "fixture-password"
		host, err := hostService.Save(ctx, actor, "", hosts.Input{Name: name, Address: "example.com", Port: 22, Username: "test", Password: &password})
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, host)
	}
	service, err := New(ctx, db, vault)
	if err != nil {
		t.Fatal(err)
	}
	return service, db, actor, targets
}
func jobInput(targets []store.Host) Input {
	return Input{Name: "Health check", Script: "#!/bin/sh\nprintf '%s\\n' \"$(hostname)\"\n", HostIDs: []string{targets[1].ID, targets[0].ID}, ExecutionMode: "sequential", StopOnFailure: true}
}

func TestJobLifecycleReferencesAndAtomicUpdates(t *testing.T) {
	service, db, actor, targets := jobHarness(t)
	ctx := context.Background()
	input := jobInput(targets)
	job, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	if job.Version != 1 || job.Script != input.Script || !slices.Equal(job.HostIDs, input.HostIDs) {
		t.Fatalf("incorrect definition: %+v", job)
	}
	input.Name = strings.ToUpper(input.Name)
	if _, err := service.Save(ctx, actor, "", input); !errors.Is(err, store.ErrJobName) {
		t.Fatalf("duplicate name: %v", err)
	}
	if err := db.DeleteHost(ctx, actor, targets[0].ID, 1); !errors.Is(err, store.ErrHostInUse) {
		t.Fatalf("assigned host deleted: %v", err)
	}
	// A failed reference check leaves both the script and target order intact.
	input.Name, input.Version = "Updated job", job.Version
	input.Script = "echo changed\n"
	input.HostIDs = []string{targets[0].ID, strings.Repeat("f", 32)}
	if _, err := service.Save(ctx, actor, job.ID, input); !errors.Is(err, store.ErrJobHost) {
		t.Fatalf("missing target: %v", err)
	}
	listed, err := service.List(ctx, actor)
	if err != nil || len(listed) != 1 || listed[0].Script != job.Script || !slices.Equal(listed[0].HostIDs, job.HostIDs) {
		t.Fatalf("failed update was not atomic: %+v %v", listed, err)
	}
	input.HostIDs = []string{targets[0].ID}
	input.ExecutionMode, input.StopOnFailure = "parallel", false
	updated, err := service.Save(ctx, actor, job.ID, input)
	if err != nil || updated.Version != 2 || updated.CreatedAt != job.CreatedAt || updated.ExecutionMode != "parallel" || updated.StopOnFailure {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if _, err := service.Save(ctx, actor, job.ID, input); !errors.Is(err, store.ErrJobChanged) {
		t.Fatalf("stale update: %v", err)
	}
	if err := service.Delete(ctx, actor, job.ID, 1); !errors.Is(err, store.ErrJobChanged) {
		t.Fatalf("stale delete: %v", err)
	}
	// Replacing targets releases the removed host, but protects the remaining one.
	if err := db.DeleteHost(ctx, actor, targets[1].ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, actor, job.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteHost(ctx, actor, targets[0].ID, 1); err != nil {
		t.Fatalf("target links survived deletion: %v", err)
	}
	if err := service.Delete(ctx, actor, job.ID, updated.Version); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing delete: %v", err)
	}
	listed, err = service.List(ctx, actor)
	if err != nil || listed == nil || len(listed) != 0 {
		t.Fatalf("empty list: %+v %v", listed, err)
	}
}

func TestJobConcurrentEdits(t *testing.T) {
	service, _, actor, targets := jobHarness(t)
	ctx := context.Background()
	input := jobInput(targets)
	job, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Version = job.Version
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := service.Save(ctx, actor, job.ID, input); results <- err })
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, store.ErrJobChanged) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("lost update: successes %d, conflicts %d", successes, conflicts)
	}
}

func TestJobValidation(t *testing.T) {
	valid := Input{Name: "Job", Script: "echo test\n", HostIDs: []string{strings.Repeat("a", 32)}, ExecutionMode: "parallel"}
	cases := []struct {
		name   string
		change func(*Input)
	}{
		{"empty name", func(i *Input) { i.Name = " " }},
		{"long name", func(i *Input) { i.Name = strings.Repeat("a", 101) }},
		{"control name", func(i *Input) { i.Name = "bad\x00name" }},
		{"invalid UTF-8 name", func(i *Input) { i.Name = "\xff" }},
		{"empty script", func(i *Input) { i.Script = " \n\t" }},
		{"null script", func(i *Input) { i.Script = "echo\x00bad" }},
		{"invalid UTF-8 script", func(i *Input) { i.Script = "\xff" }},
		{"large script", func(i *Input) { i.Script = strings.Repeat("a", MaxScriptBytes+1) }},
		{"multibyte script size", func(i *Input) { i.Script = strings.Repeat("\u00e9", MaxScriptBytes/2+1) }},
		{"empty hosts", func(i *Input) { i.HostIDs = nil }},
		{"many hosts", func(i *Input) { i.HostIDs = make([]string, 1001) }},
		{"invalid host", func(i *Input) { i.HostIDs = []string{"invalid"} }},
		{"duplicate host", func(i *Input) { i.HostIDs = []string{strings.Repeat("a", 32), strings.Repeat("a", 32)} }},
		{"noncanonical host", func(i *Input) { i.HostIDs = []string{strings.Repeat("A", 32)} }},
		{"empty tag", func(i *Input) { i.HostTags = []string{" "} }},
		{"long tag", func(i *Input) { i.HostTags = []string{strings.Repeat("a", 51)} }},
		{"control tag", func(i *Input) { i.HostTags = []string{"one\nbad"} }},
		{"comma tag", func(i *Input) { i.HostTags = []string{"one,two"} }},
		{"invalid UTF-8 tag", func(i *Input) { i.HostTags = []string{"\xff"} }},
		{"many tags", func(i *Input) { i.HostTags = make([]string, 1001) }},
		{"invalid exclusion", func(i *Input) { i.ExcludedHostIDs = []string{"invalid"} }},
		{"duplicate exclusion", func(i *Input) { i.ExcludedHostIDs = []string{strings.Repeat("b", 32), strings.Repeat("b", 32)} }},
		{"selected exclusion", func(i *Input) { i.ExcludedHostIDs = i.HostIDs }},
		{"many exclusions", func(i *Input) { i.ExcludedHostIDs = make([]string, 1001) }},
		{"invalid mode", func(i *Input) { i.ExecutionMode = "invalid" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.change(&input)
			if _, err := validate(input); !errors.Is(err, ErrInput) {
				t.Fatalf("invalid input accepted: %v", err)
			}
		})
	}
	valid.Name = "\u0414\u043e\u043c"
	valid.Script = "printf '\u65e5\u672c\\n'\n"
	job, err := validate(valid)
	if err != nil || job.Script != valid.Script {
		t.Fatalf("Unicode script changed: %v", err)
	}
	valid.Script = strings.Repeat("x", MaxScriptBytes)
	if _, err := validate(valid); err != nil {
		t.Fatal(err)
	}
}

func TestJobAuthorizationAndIDs(t *testing.T) {
	service, db, actor, targets := jobHarness(t)
	ctx := context.Background()
	input := jobInput(targets)
	job, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Version = 1
	if _, err := service.Save(ctx, actor, "bad", input); !errors.Is(err, ErrInput) {
		t.Fatal("invalid update ID accepted")
	}
	input.Version = 0
	if _, err := service.Save(ctx, actor, job.ID, input); !errors.Is(err, ErrInput) {
		t.Fatal("unversioned update accepted")
	}
	input.Version = 1
	if _, err := service.Save(ctx, actor, "", input); !errors.Is(err, ErrInput) {
		t.Fatal("versioned create accepted")
	}
	if err := service.Delete(ctx, actor, "bad", 1); !errors.Is(err, ErrInput) {
		t.Fatal("invalid delete ID accepted")
	}
	if err := service.Delete(ctx, actor, job.ID, 0); !errors.Is(err, ErrInput) {
		t.Fatal("unversioned delete accepted")
	}
	input.Version = 0
	key, token, err := auth.NewAPIKey(actor.User, "Read only", []string{store.ScopeJobsRead}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateLocalAPIKey(ctx, actor.User, key, auth.TokenHash(token)); err != nil {
		t.Fatal(err)
	}
	readActor, err := db.ResolveAPIKey(ctx, auth.TokenHash(token), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(ctx, readActor); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Save(ctx, readActor, "", input); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("scope ignored: %v", err)
	}
	if err := service.Delete(ctx, readActor, job.ID, 1); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("scope ignored: %v", err)
	}
	if err := db.RevokeLocalAPIKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(ctx, readActor); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("revoked key accepted: %v", err)
	}
	if _, err := service.Save(ctx, readActor, "", input); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("revoked key create: %v", err)
	}
	if err := service.Delete(ctx, readActor, job.ID, 1); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("revoked key delete: %v", err)
	}
	if err := db.DeleteSession(ctx, actor.SessionHash); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(ctx, actor); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("revoked session read: %v", err)
	}
	if _, err := service.Save(ctx, actor, "", input); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("revoked session create: %v", err)
	}
	input.Version = 1
	if _, err := service.Save(ctx, actor, job.ID, input); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("revoked session update: %v", err)
	}
	if err := service.Delete(ctx, actor, job.ID, 1); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("revoked session delete: %v", err)
	}
}

func TestJobTagOnlyTargetsAndCanonicalTags(t *testing.T) {
	input := Input{Name: "Tag job", Script: "true", HostTags: []string{" prod ", "dev", "prod", "\u65e5\u672c"}, ExecutionMode: "sequential"}
	job, err := validate(input)
	if err != nil || len(job.HostIDs) != 0 || !slices.Equal(job.HostTags, []string{"dev", "prod", "\u65e5\u672c"}) {
		t.Fatalf("invalid canonical tags: %+v %v", job, err)
	}
}
