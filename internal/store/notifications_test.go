package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"piped/internal/secrets"
	"reflect"
	"strings"
	"testing"
)

func TestJobNotificationDefaultsPreservationAndSnapshot(t *testing.T) {
	db, actor, original := scheduledJobFixture(t)
	ctx := context.Background()
	create := func(id string, enabled, byDefault bool) NotificationTarget {
		target, err := db.SaveNotificationTarget(ctx, actor, NotificationTarget{Type: "webhook", ID: id, Name: id, Method: "POST", Body: "$job", ContentType: "text/plain", Enabled: enabled, DefaultEnabled: byDefault, Endpoint: &NotificationEndpoint{URL: "https://" + id + ".example"}, Secrets: []NotificationSecret{{Name: "token", Ciphertext: []byte("encrypted-" + id)}}}, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	target := create("default", true, true)
	create("disabled", false, true)
	create("optional", true, false)
	original.ID, original.Name, original.Version, original.NotificationTargetIDs = "new", "New", 0, nil
	job, err := db.SaveJob(ctx, actor, original, nil, true)
	if err != nil || !reflect.DeepEqual(job.NotificationTargetIDs, []string{"default"}) {
		t.Fatalf("defaults: %+v %v", job, err)
	}
	job.NotificationTargetIDs = nil
	job, err = db.SaveJob(ctx, actor, job, nil, false)
	if err != nil || !reflect.DeepEqual(job.NotificationTargetIDs, []string{"default"}) {
		t.Fatalf("omission lost selection: %+v %v", job, err)
	}
	_, _, connections, err := db.CreateRun(ctx, actor, "snapshot-notifications", job.ID, job.Version)
	if err != nil || len(connections) == 0 || len(connections[0].Notifications) != 1 || !bytes.Equal(connections[0].Notifications[0].Secrets[0].Ciphertext, []byte("encrypted-default")) {
		t.Fatalf("snapshot: %v", err)
	}
	target.Enabled = false
	target, err = db.SaveNotificationTarget(ctx, actor, target, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !connections[0].Notifications[0].Enabled {
		t.Fatal("target edit mutated snapshot")
	}
	_, _, next, err := db.CreateRun(ctx, actor, "disabled-notifications", job.ID, job.Version)
	if err != nil || len(next[0].Notifications) != 0 {
		t.Fatalf("disabled target delivered: %v", err)
	}
	if err := db.DeleteNotificationTarget(ctx, actor, target.ID, target.Version); !errors.Is(err, ErrNotificationInUse) {
		t.Fatalf("assigned deletion: %v", err)
	}
	copy := job
	copy.ID, copy.Name, copy.Version, copy.NotificationTargetIDs = "copy", "Copy", 0, nil
	copied, err := db.CopyJob(ctx, actor, copy, nil, JobCopy{ID: job.ID, Version: job.Version}, func(values []JobSecret) ([]JobSecret, error) { return values, nil })
	if err != nil || !reflect.DeepEqual(copied.NotificationTargetIDs, []string{"default"}) {
		t.Fatalf("copy selection: %+v %v", copied, err)
	}
	job.NotificationTargetIDs = []string{}
	job, err = db.SaveJob(ctx, actor, job, nil, false)
	if err != nil || len(job.NotificationTargetIDs) != 0 {
		t.Fatalf("explicit empty: %v", err)
	}
	copied.NotificationTargetIDs = []string{}
	if _, err := db.SaveJob(ctx, actor, copied, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteNotificationTarget(ctx, actor, target.ID, target.Version); err != nil {
		t.Fatal(err)
	}
	// The immutable admitted connection remains usable even after deletion.
	if !bytes.Equal(connections[0].Notifications[0].Secrets[0].Ciphertext, []byte("encrypted-default")) {
		t.Fatal("snapshot lost endpoint")
	}
	before, err := db.Jobs(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	job.NotificationTargetIDs = []string{"missing"}
	job.Script = "changed"
	if _, err := db.SaveJob(ctx, actor, job, nil, false); !errors.Is(err, ErrJobNotification) {
		t.Fatalf("missing target: %v", err)
	}
	after, err := db.Jobs(ctx, actor)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("missing target did not roll back job")
	}
}

func TestNotificationOutputTailIncludesSmallChunksAndIsBounded(t *testing.T) {
	db, actor, job := scheduledJobFixture(t)
	ctx := context.Background()
	run, _, connections, err := db.CreateRun(ctx, actor, "tail", job.ID, job.Version)
	if err != nil {
		t.Fatal(err)
	}
	hostID := connections[0].Host.ID
	for range 300 {
		if err := db.AppendOutput(ctx, run.ID, hostID, []byte("a")); err != nil {
			t.Fatal(err)
		}
	}
	output, err := db.NotificationOutput(ctx, run.ID, hostID)
	if err != nil || output != strings.Repeat("a", 300) {
		t.Fatalf("small chunks truncated: %d %v", len(output), err)
	}
	for range 5 {
		if err := db.AppendOutput(ctx, run.ID, hostID, bytes.Repeat([]byte("z"), MaxOutputBatchBytes)); err != nil {
			t.Fatal(err)
		}
	}
	output, err = db.NotificationOutput(ctx, run.ID, hostID)
	if err != nil || output != strings.Repeat("z", 256*1024) {
		t.Fatalf("output not bounded: %d %v", len(output), err)
	}
}

func TestJobNotificationDefaultLimitRollsBack(t *testing.T) {
	db, actor, job := scheduledJobFixture(t)
	ctx := context.Background()
	for index := 0; index <= MaxJobNotificationTargets; index++ {
		_, err := db.SaveNotificationTarget(ctx, actor, NotificationTarget{Type: "webhook", ID: fmt.Sprint(index), Name: "Hook", Method: "POST", ContentType: "text/plain", Enabled: true, DefaultEnabled: true, Endpoint: &NotificationEndpoint{URL: "https://example.internal"}}, true, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	job.ID, job.Name, job.Version, job.NotificationTargetIDs = "too-many", "Too many", 0, nil
	if _, err := db.SaveJob(ctx, actor, job, nil, true); !errors.Is(err, ErrJobNotificationLimit) {
		t.Fatalf("unbounded defaults: %v", err)
	}
	jobs, err := db.Jobs(ctx, actor)
	if err != nil || len(jobs) != 1 {
		t.Fatal("default limit did not roll back creation")
	}
}

func TestNotificationReadableEndpointAndEncryptedSecretStorage(t *testing.T) {
	db, actor, _ := scheduledJobFixture(t)
	ctx := context.Background()
	vault, err := secrets.New(bytes.Repeat([]byte{14}, 32))
	if err != nil {
		t.Fatal(err)
	}
	value := []byte("notification-private-token")
	ciphertext := vault.Seal("notification:readable:secret:token", value)
	target, err := db.SaveNotificationTarget(ctx, actor, NotificationTarget{
		Type: "webhook", ID: "readable", Name: "Readable", Method: "POST", ContentType: "text/plain", Enabled: true,
		Endpoint: &NotificationEndpoint{URL: "https://example.internal/$token", Headers: map[string]string{"Authorization": "Bearer $token"}},
		Secrets:  []NotificationSecret{{Name: "token", Ciphertext: ciphertext}},
	}, true, nil)
	if err != nil || len(target.Secrets) != 0 || len(target.SecretNames) != 1 {
		t.Fatalf("invalid saved metadata: %+v %v", target, err)
	}
	var endpoint string
	var stored []byte
	if err := db.db.QueryRowContext(ctx, `SELECT endpoint FROM notification_targets WHERE id = ?`, target.ID).Scan(&endpoint); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(endpoint, "$token") || strings.Contains(endpoint, string(value)) {
		t.Fatal("endpoint templates were not stored as plain text")
	}
	if err := db.db.QueryRowContext(ctx, `SELECT encrypted_value FROM notification_secrets WHERE target_id = ? AND name = 'token'`, target.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	plain, err := vault.Open("notification:readable:secret:token", stored)
	if err != nil || !bytes.Equal(plain, value) || bytes.Contains(stored, value) {
		t.Fatal("secret was not encrypted and bound to its target/name")
	}
	if _, err := db.NotificationTargets(ctx, Actor{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthenticated read: %v", err)
	}
	invalidActor := target
	if _, err := db.SaveNotificationTarget(ctx, Actor{}, invalidActor, false, nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthenticated save: %v", err)
	}
	changedType := target
	changedType.Type = "email"
	if _, err := db.SaveNotificationTarget(ctx, actor, changedType, false, nil); !errors.Is(err, ErrNotificationType) {
		t.Fatalf("mutable type: %v", err)
	}
	listed, err := db.NotificationTargets(ctx, actor)
	if err != nil || len(listed) != 1 || listed[0].Endpoint.URL != target.Endpoint.URL || len(listed[0].Secrets) != 0 || len(listed[0].SecretNames) != 1 {
		t.Fatalf("invalid readable metadata: %+v %v", listed, err)
	}
	stale := target
	target.Endpoint = nil
	target.Secrets = []NotificationSecret{{Name: "token"}}
	target, err = db.SaveNotificationTarget(ctx, actor, target, false, nil)
	if err != nil || target.Endpoint == nil {
		t.Fatalf("omission lost templates or secrets: %v", err)
	}
	if _, err := db.SaveNotificationTarget(ctx, actor, stale, false, nil); !errors.Is(err, ErrNotificationChanged) {
		t.Fatalf("stale save: %v", err)
	}
	if err := db.DeleteNotificationTarget(ctx, actor, stale.ID, stale.Version); !errors.Is(err, ErrNotificationChanged) {
		t.Fatalf("stale delete: %v", err)
	}
	missing := target
	missing.Secrets = []NotificationSecret{{Name: "missing"}}
	if _, err := db.SaveNotificationTarget(ctx, actor, missing, false, nil); !errors.Is(err, ErrNotificationSecret) {
		t.Fatalf("new secret without value: %v", err)
	}
	if err := db.DeleteNotificationTarget(ctx, actor, target.ID, target.Version); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_secrets WHERE target_id = ?`, target.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("target deletion retained secrets")
	}
}
