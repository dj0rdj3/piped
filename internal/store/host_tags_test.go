package store

import (
	"context"
	"reflect"
	"testing"
)

func TestCanonicalTagsIgnoreLegacyJSONAndDriveAllMembership(t *testing.T) {
	db, actor, job := admissionFixture(t)
	ctx := context.Background()
	hosts, err := db.Hosts(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	host := hosts[0]
	host.Tags = []string{"canonical", "Web"}
	if _, err := db.SaveHost(ctx, actor, host, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	// The old column is no longer read after the atomic upgrade marker.
	if _, err := db.db.ExecContext(ctx, `UPDATE hosts SET tags = ? WHERE id = ?`, `["legacy"]`, host.ID); err != nil {
		t.Fatal(err)
	}
	job.HostIDs = []string{}
	job.HostTags = []string{"canonical"}
	job, err = db.SaveJob(ctx, actor, job, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	hosts, err = db.Hosts(ctx, actor)
	if err != nil || !reflect.DeepEqual(hosts[0].Tags, []string{"Web", "canonical"}) {
		t.Fatal("legacy JSON remained authoritative", hosts, err)
	}
	page, err := db.HostsPage(ctx, actor, ListRequest{Limit: 10, Tag: "canonical"})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("host list membership diverged", page, err)
	}
	created, err := db.AdmitRun(ctx, actor, RunAdmission{ID: "canonical-run", RequestID: "canonical-intent", JobID: job.ID, Version: job.Version})
	if err != nil || len(created.PublicRun().Hosts) != 1 {
		t.Fatal("admission ignored relational tags", err)
	}
	jobs, err := db.JobsPage(ctx, actor, ListRequest{Limit: 10, Query: host.Name})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatal("job search membership diverged", jobs, err)
	}
}

func TestHostTagsUpgradeIsAtomicAndRunsOnce(t *testing.T) {
	db, actor, _ := admissionFixture(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `UPDATE hosts SET tags = ?`, `["Web","web","Web"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DELETE FROM list_state WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := db.initializeHostTags(ctx); err != nil {
		t.Fatal(err)
	}
	hosts, err := db.Hosts(ctx, actor)
	if err != nil || !reflect.DeepEqual(hosts[0].Tags, []string{"Web", "web"}) {
		t.Fatal("upgrade changed case or failed to deduplicate", hosts, err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE hosts SET tags = ?`, `invalid-json`); err != nil {
		t.Fatal(err)
	}
	if err := db.initializeHostTags(ctx); err != nil {
		t.Fatal("upgrade ran again despite durable marker", err)
	}
	if _, err := db.db.ExecContext(ctx, `DELETE FROM list_state WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := db.initializeHostTags(ctx); err == nil {
		t.Fatal("corrupt upgrade was accepted")
	}
	hosts, err = db.Hosts(ctx, actor)
	if err != nil || !reflect.DeepEqual(hosts[0].Tags, []string{"Web", "web"}) {
		t.Fatal("failed upgrade destroyed existing membership", hosts, err)
	}
	var markers int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM list_state WHERE version = 2`).Scan(&markers); err != nil || markers != 0 {
		t.Fatal("failed conversion recorded completion", err)
	}
}
