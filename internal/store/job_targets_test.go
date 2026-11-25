package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestDynamicJobTargetsSnapshotFutureMembership(t *testing.T) {
	db, actor, job := scheduledJobFixture(t)
	ctx := context.Background()
	saveHost := func(id, name string, tags []string, version int64) Host {
		t.Helper()
		host, err := db.SaveHost(ctx, actor, Host{ID: id, Name: name, Address: "localhost", Port: 22, Username: "fixture", Tags: tags, Version: version}, []byte("encrypted password"), nil, version == 0)
		if err != nil {
			t.Fatal(err)
		}
		return host
	}
	// The explicit host also matches both tags and must still run exactly once.
	saveHost("host", "Host", []string{"web", "production"}, 1)
	alpha := saveHost("alpha", "Alpha", []string{"web", "production"}, 0)
	saveHost("other", "Other", []string{"Web"}, 0)
	job.HostTags = []string{"production", "web"}
	var err error
	job, err = db.SaveJob(ctx, actor, job, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := db.Jobs(ctx, actor)
	if err != nil || !slices.Equal(listed[0].HostTags, job.HostTags) {
		t.Fatalf("lost tags: %+v %v", listed, err)
	}
	check := func(run Run, connections []Connection, err error, want []string) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for i, host := range run.Hosts {
			ids = append(ids, host.HostID)
			if connections[i].Host.ID != host.HostID {
				t.Fatal("connection ordering differs from snapshot")
			}
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("targets = %v; want %v", ids, want)
		}
	}
	first, _, connections, err := db.CreateRun(ctx, actor, "first", job.ID, job.Version)
	check(first, connections, err, []string{"host", "alpha"})
	saveHost("beta", "Beta", []string{"web"}, 0)
	saveHost(alpha.ID, alpha.Name, []string{}, alpha.Version)
	second, _, connections, err := db.CreateRun(ctx, actor, "second", job.ID, job.Version)
	check(second, connections, err, []string{"host", "beta"})
	old, err := db.Run(ctx, actor, first.ID)
	if err != nil || len(old.Hosts) != 2 || old.Hosts[1].HostID != "alpha" {
		t.Fatal("tag edit changed an admitted run", err)
	}
	for _, run := range []Run{first, second} {
		if err := db.FinishRun(ctx, run.ID, "succeeded"); err != nil {
			t.Fatal(err)
		}
	}
	saveHost("gamma", "Gamma", []string{"production"}, 0)
	at := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	scheduled, _, connections, err := db.CreateScheduledRun(ctx, "scheduled", job.ID, job.Version, at.Unix(), func() time.Time { return at })
	check(scheduled, connections, err, []string{"host", "beta", "gamma"})
}

func TestTagOnlyJobsAllowFutureHostsAndRejectEmptyOrOversizedRuns(t *testing.T) {
	db, actor, _ := scheduledJobFixture(t)
	ctx := context.Background()
	job, err := db.SaveJob(ctx, actor, Job{ID: "future", Name: "Future tag", Script: "true", HostTags: []string{"future"}, ExecutionMode: "sequential"}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.CreateRun(ctx, actor, "empty", job.ID, job.Version); !errors.Is(err, ErrJobTargets) {
		t.Fatalf("empty targets accepted: %v", err)
	}
	if _, err := db.Run(ctx, actor, "empty"); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejected run was persisted", err)
	}
	// Use a single fixture transaction to cover the resolver's host limit on all drivers.
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i <= MaxJobHosts; i++ {
		id := fmt.Sprintf("future-%04d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO hosts (id, name, address, port, username, run_as_sudo, tags, encrypted_password, version, created_at) VALUES (?, ?, 'localhost', 22, 'fixture', 0, ?, ?, 1, 0)`, id, id, `["future"]`, []byte("encrypted password")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.CreateRun(ctx, actor, "oversized", job.ID, job.Version); !errors.Is(err, ErrJobTargets) {
		t.Fatalf("oversized targets accepted: %v", err)
	}
	job.ExcludedHostIDs = []string{"future-1000"}
	job, err = db.SaveJob(ctx, actor, job, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	run, _, connections, err := db.CreateRun(ctx, actor, "bounded", job.ID, job.Version)
	if err != nil || len(run.Hosts) != MaxJobHosts || len(connections) != MaxJobHosts {
		t.Fatalf("limit failed: hosts=%d error=%v", len(run.Hosts), err)
	}
}

func TestJobHostExclusionsPreserveTagsAndFutureMembership(t *testing.T) {
	db, actor, job := scheduledJobFixture(t)
	ctx := context.Background()
	addHost := func(id string) {
		t.Helper()
		_, err := db.SaveHost(ctx, actor, Host{ID: id, Name: id, Address: "localhost", Port: 22, Username: "fixture", Tags: []string{"web", "production"}}, []byte("encrypted password"), nil, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	addHost("alpha")
	addHost("excluded")
	job.HostTags = []string{"production", "web"}
	job.ExcludedHostIDs = []string{"excluded"}
	job, err := db.SaveJob(ctx, actor, job, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	check := func(run Run, connections []Connection, err error, want []string) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for index, host := range run.Hosts {
			ids = append(ids, host.HostID)
			if connections[index].Host.ID != host.HostID {
				t.Fatal("connection snapshot differed")
			}
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("targets = %v; want %v", ids, want)
		}
		if err := db.FinishRun(ctx, run.ID, "succeeded"); err != nil {
			t.Fatal(err)
		}
	}
	first, _, connections, err := db.CreateRun(ctx, actor, "excluded-first", job.ID, job.Version)
	check(first, connections, err, []string{"host", "alpha"})
	addHost("beta")
	second, _, connections, err := db.CreateRun(ctx, actor, "excluded-next", job.ID, job.Version)
	check(second, connections, err, []string{"host", "alpha", "beta"})
	at := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	scheduled, _, connections, err := db.CreateScheduledRun(ctx, "excluded-scheduled", job.ID, job.Version, at.Unix(), func() time.Time { return at })
	check(scheduled, connections, err, []string{"host", "alpha", "beta"})
	listed, err := db.Jobs(ctx, actor)
	if err != nil || !slices.Equal(listed[0].HostTags, job.HostTags) || !slices.Equal(listed[0].ExcludedHostIDs, []string{"excluded"}) {
		t.Fatalf("saved targets changed: %+v %v", listed, err)
	}
	job.ExcludedHostIDs = []string{"missing"}
	if _, err := db.SaveJob(ctx, actor, job, nil, false); !errors.Is(err, ErrJobHost) {
		t.Fatalf("missing exclusion accepted: %v", err)
	}
	job.ExcludedHostIDs = []string{}
	job, err = db.SaveJob(ctx, actor, job, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	restored, _, connections, err := db.CreateRun(ctx, actor, "excluded-restored", job.ID, job.Version)
	check(restored, connections, err, []string{"host", "alpha", "beta", "excluded"})
	old, err := db.Run(ctx, actor, first.ID)
	if err != nil || len(old.Hosts) != 2 {
		t.Fatal("editing exclusions changed saved history", err)
	}
	job.ExcludedHostIDs = []string{"excluded"}
	if _, err := db.SaveJob(ctx, actor, job, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteHost(ctx, actor, "excluded", 1); err != nil {
		t.Fatal("exclusion prevented host deletion", err)
	}
	listed, err = db.Jobs(ctx, actor)
	if err != nil || len(listed[0].ExcludedHostIDs) != 0 {
		t.Fatal("deleted host left an exclusion", err)
	}
}
