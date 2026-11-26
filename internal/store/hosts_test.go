package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestConcurrentHostChangesAndNames(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, db, user, "session")
	host := Host{ID: "one", Name: "Server", Address: "example.com", Port: 22, Username: "deploy", Tags: []string{}}
	host, err = db.SaveHost(ctx, actor, host, []byte("ciphertext"), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() { defer group.Done(); _, err := db.SaveHost(ctx, actor, host, nil, nil, false); errs <- err }()
	}
	group.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrHostChanged) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("concurrent edits did not reject stale version")
	}
	duplicate := host
	duplicate.ID = "two"
	duplicate.Name = "server"
	if _, err := db.SaveHost(ctx, actor, duplicate, []byte("ciphertext"), nil, true); !errors.Is(err, ErrHostName) {
		t.Fatal("case-insensitive duplicate accepted")
	}
	duplicate.ID = "missing"
	duplicate.Version = 1
	if _, err := db.SaveHost(ctx, actor, duplicate, []byte("ciphertext"), nil, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing update accepted")
	}
}

func TestHostTransactionsRecheckAPIKeyScopesAndRevocation(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	key := APIKey{ID: "key", UserID: user.ID, Name: "test", Scopes: []string{ScopeHostsWrite}, CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := db.CreateLocalAPIKey(ctx, user, key, "keyhash"); err != nil {
		t.Fatal(err)
	}
	actor, err := db.ResolveAPIKey(ctx, "keyhash", time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	host := Host{ID: "one", Name: "Server", Address: "example.com", Port: 22, Username: "deploy", Tags: []string{}}
	host, err = db.SaveHost(ctx, actor, host, []byte("ciphertext"), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Hosts(ctx, actor); !errors.Is(err, ErrForbidden) {
		t.Fatal("write scope implied read")
	}
	if _, err := db.db.Exec("UPDATE api_keys SET scopes = ? WHERE id = ?", ScopeHostsRead, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveHost(ctx, actor, host, nil, nil, false); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale scopes accepted by update")
	}
	if err := db.DeleteHost(ctx, actor, host.ID, host.Version); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale scopes accepted by delete")
	}
	if err := db.RevokeLocalAPIKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveHost(ctx, actor, host, nil, nil, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked API key accepted")
	}
	if _, err := db.Hosts(ctx, actor); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked API read accepted")
	}
}

func TestFreshSchemaPreservesExplicitSudoPolicy(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, db, user, "session")
	host, err := db.SaveHost(ctx, actor, Host{
		ID: "target", Name: "Target", Address: "localhost", Port: 22,
		Username: "user", RunAsSudo: true, Tags: []string{},
	}, []byte("ciphertext"), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	job, err := db.SaveJob(ctx, actor, Job{
		ID: "job", Name: "New job", Script: "echo test",
		HostIDs: []string{host.ID}, ExecutionMode: "sequential",
	}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if job.RunAsSudo || job.Version != 1 {
		t.Fatalf("unexpected fresh job policy: %+v", job)
	}
	runAdmission, err := db.AdmitRun(ctx, actor, RunAdmission{ID: "run", RequestID: "run", JobID: job.ID, Version: job.Version})
	run := runAdmission.PublicRun()
	if err != nil {
		t.Fatal(err)
	}
	if run.RunAsSudo {
		t.Fatal("run elevated without the job's sudo setting")
	}
	if err := db.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := db.Jobs(ctx, actor)
	if err != nil || len(jobs) != 1 || jobs[0].RunAsSudo || jobs[0].Version != job.Version {
		t.Fatalf("reopening schema changed the job policy: %+v %v", jobs, err)
	}
	history, err := db.Run(ctx, actor, run.ID)
	if err != nil || history.RunAsSudo {
		t.Fatalf("reopening schema changed the run snapshot: %+v %v", history, err)
	}
}

func TestHostCredentialReferencesAndDeletionRollback(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, db, user, "session")
	credential, err := db.CreateCredential(ctx, actor, Credential{ID: "key", Name: "Key"}, []byte("encrypted private key"))
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.SaveHost(ctx, actor, Host{
		ID: "target", Name: "Target", Address: "localhost", Port: 22, Username: "deploy",
		RunAsSudo: true, Tags: []string{"original"},
	}, []byte("encrypted password"), []byte("encrypted sudo password"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, create := range []bool{true, false} {
		candidate := host
		candidate.CredentialID = "missing"
		candidate.Name, candidate.Address, candidate.RunAsSudo = "Changed", "changed.example", false
		candidate.Tags = []string{"changed"}
		if create {
			candidate.ID, candidate.Version = "new", 0
		}
		if _, err := db.SaveHost(ctx, actor, candidate, nil, nil, create); !errors.Is(err, ErrHostCredential) {
			t.Fatalf("missing credential, create=%t: %v", create, err)
		}
		listed, err := db.Hosts(ctx, actor)
		if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], host) {
			t.Fatalf("failed reference changed the host: %+v, %v", listed, err)
		}
	}
	job, err := db.SaveJob(ctx, actor, Job{ID: "job", Name: "Job", Script: "echo fixture", HostIDs: []string{host.ID}, ExecutionMode: "sequential", RunAsSudo: true}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	connectionsAdmission, err := db.AdmitRun(ctx, actor, RunAdmission{ID: "snapshot", RequestID: "snapshot", JobID: job.ID, Version: job.Version})
	connections := connectionsAdmission.(CreatedRun).Connections
	if err != nil || len(connections) != 1 || !bytes.Equal(connections[0].Password, []byte("encrypted password")) || !bytes.Equal(connections[0].SudoPassword, []byte("encrypted sudo password")) {
		t.Fatalf("rejected writes changed encrypted secrets: %v", err)
	}
	if err := db.ApproveHostKey(ctx, actor, host.ID, host.Address, host.Port, "approved key"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteHost(ctx, actor, host.ID, host.Version+1); !errors.Is(err, ErrHostChanged) {
		t.Fatalf("stale delete did not take precedence: %v", err)
	}
	if err := db.DeleteHost(ctx, actor, host.ID, host.Version); !errors.Is(err, ErrHostInUse) {
		t.Fatalf("assigned host deletion: %v", err)
	}
	key, err := db.TrustedHostKey(ctx, host.ID, host.Address, host.Port)
	if err != nil || key != "approved key" {
		t.Fatalf("failed deletion removed host-key approval: %q, %v", key, err)
	}
	host.CredentialID = credential.ID
	host, err = db.SaveHost(ctx, actor, host, nil, nil, false)
	if err != nil || host.HasPassword || !host.HasSudoPassword || host.CredentialName != credential.Name {
		t.Fatalf("credential switch: %+v, %v", host, err)
	}
	if err := db.DeleteCredential(ctx, actor, credential.ID); !errors.Is(err, ErrCredentialInUse) {
		t.Fatalf("assigned credential deletion: %v", err)
	}
	if err := db.RenameCredential(ctx, actor, credential.ID, "Renamed"); err != nil {
		t.Fatal(err)
	}
	listed, err := db.Hosts(ctx, actor)
	if err != nil || len(listed) != 1 || listed[0].CredentialName != "Renamed" || listed[0].Version != host.Version {
		t.Fatalf("referenced metadata changed unexpectedly: %+v, %v", listed, err)
	}
	if err := db.DeleteJob(ctx, actor, job.ID, job.Version); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteHost(ctx, actor, host.ID, host.Version); err != nil {
		t.Fatal(err)
	}
	key, err = db.TrustedHostKey(ctx, host.ID, host.Address, host.Port)
	if err != nil || key != "" {
		t.Fatalf("host deletion retained host-key approval: %q, %v", key, err)
	}
	if err := db.DeleteCredential(ctx, actor, credential.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteCredential(ctx, actor, credential.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing credential deletion: %v", err)
	}
	history, err := db.Run(ctx, actor, "snapshot")
	if err != nil || len(history.Hosts) != 1 || history.Hosts[0].Name != "Target" || !bytes.Equal(connections[0].Password, []byte("encrypted password")) {
		t.Fatalf("deletion changed admitted history or connections: %+v, %v", history, err)
	}
}

func TestConcurrentCredentialDeletionAndHostAssignment(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, db, user, "session")
	for index := range 8 {
		id := fmt.Sprint(index)
		credential, err := db.CreateCredential(ctx, actor, Credential{ID: "key" + id, Name: "Key " + id}, []byte("encrypted private key"))
		if err != nil {
			t.Fatal(err)
		}
		host, err := db.SaveHost(ctx, actor, Host{ID: "host" + id, Name: "Host " + id, Address: "localhost", Port: 22, Username: "deploy", Tags: []string{}}, []byte("encrypted password"), nil, true)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		assigned, deleted := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			host.CredentialID = credential.ID
			_, err := db.SaveHost(ctx, actor, host, nil, nil, false)
			assigned <- err
		}()
		go func() {
			<-start
			deleted <- db.DeleteCredential(ctx, actor, credential.ID)
		}()
		close(start)
		assignErr, deleteErr := <-assigned, <-deleted
		if !((assignErr == nil && errors.Is(deleteErr, ErrCredentialInUse)) || (errors.Is(assignErr, ErrHostCredential) && deleteErr == nil)) {
			t.Fatalf("assignment/delete did not serialize: assignment=%v, deletion=%v", assignErr, deleteErr)
		}
		var reference sql.NullString
		var password []byte
		if err := db.db.QueryRowContext(ctx, `SELECT credential_id, encrypted_password FROM hosts WHERE id = ?`, host.ID).Scan(&reference, &password); err != nil {
			t.Fatal(err)
		}
		if assignErr == nil {
			if reference.String != credential.ID || password != nil {
				t.Fatal("successful key assignment retained a password or lost its reference")
			}
		} else if reference.Valid || !bytes.Equal(password, []byte("encrypted password")) {
			t.Fatal("rejected assignment changed authentication")
		}
	}
}

func TestConcurrentHostDeletionAndJobAssignment(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, db, user, "session")
	for index := range 8 {
		id := fmt.Sprint(index)
		host, err := db.SaveHost(ctx, actor, Host{ID: "host" + id, Name: "Host " + id, Address: "localhost", Port: 22, Username: "deploy", Tags: []string{}}, []byte("encrypted password"), nil, true)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		assigned, deleted := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, err := db.SaveJob(ctx, actor, Job{ID: "job" + id, Name: "Job " + id, Script: "echo fixture", HostIDs: []string{host.ID}, ExecutionMode: "sequential"}, nil, true)
			assigned <- err
		}()
		go func() {
			<-start
			deleted <- db.DeleteHost(ctx, actor, host.ID, host.Version)
		}()
		close(start)
		assignErr, deleteErr := <-assigned, <-deleted
		if !((assignErr == nil && errors.Is(deleteErr, ErrHostInUse)) || (errors.Is(assignErr, ErrJobHost) && deleteErr == nil)) {
			t.Fatalf("job assignment/host delete did not serialize: assignment=%v, deletion=%v", assignErr, deleteErr)
		}
		var dangling int
		if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_hosts j LEFT JOIN hosts h ON h.id = j.host_id WHERE h.id IS NULL`).Scan(&dangling); err != nil || dangling != 0 {
			t.Fatalf("dangling host references: %d, %v", dangling, err)
		}
	}
}
