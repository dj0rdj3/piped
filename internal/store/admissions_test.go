package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"piped/internal/secrets"
)

func admissionFixture(t *testing.T) (*Store, Actor, Job) {
	t.Helper()
	db := openTest(t)
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "fixture-hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, db, user, "admission-session")
	if _, err := db.SaveHost(ctx, actor, Host{ID: "host", Name: "Host", Address: "localhost", Port: 22, Username: "fixture"}, []byte("ciphertext"), nil, true); err != nil {
		t.Fatal(err)
	}
	job, err := db.SaveJob(ctx, actor, Job{ID: "job", Name: "Admission", Script: "printf fixture", HostIDs: []string{"host"}, ExecutionMode: "parallel"}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	return db, actor, job
}

func TestConcurrentAdmissionIdentityHasOneSnapshotAndNoReplayAuthority(t *testing.T) {
	db, actor, job := admissionFixture(t)
	ctx := context.Background()
	var workers sync.WaitGroup
	results := make(chan Admission, 8)
	for index := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			admitted, err := db.AdmitRun(ctx, actor, RunAdmission{ID: fmt.Sprintf("run-%d", index), JobID: job.ID, Version: job.Version, RequestID: "same-reviewed-request"})
			if err != nil {
				t.Error(err)
				return
			}
			results <- admitted
		}()
	}
	workers.Wait()
	close(results)
	var runID string
	created, replayed := 0, 0
	for admitted := range results {
		if runID != "" && admitted.PublicRun().ID != runID {
			t.Fatal("one request admitted multiple runs")
		}
		runID = admitted.PublicRun().ID
		switch result := admitted.(type) {
		case ExistingRun:
			replayed++
		case CreatedRun:
			created++
			if len(result.Connections) != 1 {
				t.Fatal("initial admission lost the reviewed snapshot")
			}
		default:
			t.Fatal("invalid admission outcome")
		}
	}
	if created != 1 || replayed != 7 {
		t.Fatal("concurrent request identity was not atomic", created, replayed)
	}
	// A later definition edit must not change the already reviewed admission.
	job.Name = "Changed after admission"
	if _, err := db.SaveJob(ctx, actor, job, nil, false); err != nil {
		t.Fatal(err)
	}
	admitted, err := db.AdmitRun(ctx, actor, RunAdmission{ID: "ignored", JobID: job.ID, Version: job.Version, RequestID: "same-reviewed-request"})
	if err != nil || admitted.PublicRun().ID != runID || admitted.PublicRun().JobName != "Admission" || !isExisting(admitted) {
		t.Fatal("retry changed or re-admitted the original request", admitted.PublicRun(), err)
	}
}

func TestAdmissionReplayRechecksAuthorityAndFingerprint(t *testing.T) {
	db, actor, job := admissionFixture(t)
	ctx := context.Background()
	input := RunAdmission{ID: "first", JobID: job.ID, Version: job.Version, RequestID: "reviewed"}
	if _, err := db.AdmitRun(ctx, actor, input); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []RunAdmission{
		{ID: "second", JobID: "other", Version: job.Version, RequestID: input.RequestID},
		{ID: "second", JobID: job.ID, Version: job.Version + 1, RequestID: input.RequestID},
	} {
		if _, err := db.AdmitRun(ctx, actor, changed); !errors.Is(err, ErrAdmissionConflict) {
			t.Fatal("request identity was rebound to a different execution", err)
		}
	}
	if err := db.DeleteSession(ctx, actor.SessionHash); err != nil {
		t.Fatal(err)
	}
	if admitted, err := db.AdmitRun(ctx, actor, input); !errors.Is(err, ErrUnauthorized) || admitted != nil {
		t.Fatal("revoked caller could read/replay an admission", err)
	}
}

func TestAdmissionKeysArePrincipalScopedAndRejectInvalidForms(t *testing.T) {
	db, actor, job := admissionFixture(t)
	ctx := context.Background()
	input := RunAdmission{ID: "first", JobID: job.ID, Version: job.Version, RequestID: "same-key"}
	if _, err := db.AdmitRun(ctx, actor, input); err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, actor, "other", "other-hash")
	if err != nil {
		t.Fatal(err)
	}
	otherActor := actorFor(t, db, other, "other-session")
	input.ID = "second"
	admitted, err := db.AdmitRun(ctx, otherActor, input)
	if err != nil || isExisting(admitted) || admitted.PublicRun().ID != "second" {
		t.Fatal("another principal collided with or read an existing request", admitted.PublicRun(), err)
	}
	for _, key := range []string{"", "with spaces", "line\nbreak", "\u00e9", strings.Repeat("a", 129)} {
		input.RequestID = key
		if _, err := db.AdmitRun(ctx, actor, input); !errors.Is(err, ErrAdmissionKey) {
			t.Fatal("invalid admission identity was accepted", err)
		}
	}
}

func TestAdmissionIdentityExpiresWithRetainedHistory(t *testing.T) {
	db, actor, job := admissionFixture(t)
	ctx := context.Background()
	request := RunAdmission{ID: "retained-run", JobID: job.ID, Version: job.Version, RequestID: "retained-identity"}
	if _, err := db.AdmitRun(ctx, actor, request); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRunHost(ctx, request.ID, "host", "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, request.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := db.PurgeRuns(ctx, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM run_admissions").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("purging history left admission tombstones", remaining, err)
	}
	request.ID = "new-intent-after-retention"
	admitted, err := db.AdmitRun(ctx, actor, request)
	if err != nil || isExisting(admitted) || admitted.PublicRun().ID != request.ID {
		t.Fatal("expired identity did not follow the documented retention boundary", admitted, err)
	}
}

func TestAdmissionResultCannotSerializeItsPrivateSnapshot(t *testing.T) {
	result := CreatedRun{
		Run: Run{ID: "public-run"}, Job: Job{Script: "private-snapshot-script"},
		Connections: []Connection{{Password: []byte("encrypted-password"), Environment: []secrets.Variable{{Name: "PRIVATE", Value: []byte("plaintext-value")}}}},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil || len(fields) != 1 || fields["run"] == nil {
		t.Fatal("admission result serialized its internal snapshot", err)
	}
}

func TestMalformedExecutionLookupIDsAreRejectedBeforeSQL(t *testing.T) {
	db, actor, job := admissionFixture(t)
	ctx := context.Background()
	for _, invalid := range []string{"bad\x00id", "bad\xffid"} {
		if err := db.RevokeUserAPIKey(ctx, actor, invalid); !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid personal key ID reached SQL: %v", err)
		}
		if _, err := db.Runs(ctx, actor, invalid); !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid history ID reached SQL: %v", err)
		}
		if _, err := db.Run(ctx, actor, invalid); !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid run ID reached SQL: %v", err)
		}
		for _, ids := range [][2]string{{invalid, "host"}, {"run", invalid}} {
			if _, err := db.RunOutput(ctx, actor, ids[0], ids[1], 0); !errors.Is(err, ErrNotFound) {
				t.Fatalf("invalid output ID reached SQL: %v", err)
			}
		}
		_, err := db.AdmitRun(ctx, actor, RunAdmission{ID: "invalid-lookup", JobID: invalid, Version: job.Version, RequestID: "invalid-lookup"})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid admission job ID reached SQL: %v", err)
		}
	}
}

func isExisting(admitted Admission) bool {
	_, ok := admitted.(ExistingRun)
	return ok
}
