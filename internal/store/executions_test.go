package store

import (
	"bytes"
	"context"
	"testing"
)

func TestOutputBatchIsAtomicBoundedAndNotifiedAfterCommit(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, db, user, "session")
	if _, err := db.SaveHost(ctx, actor, Host{ID: "host", Name: "Host", Address: "localhost", Port: 22, Username: "user"}, []byte("ciphertext"), nil, true); err != nil {
		t.Fatal(err)
	}
	job, err := db.SaveJob(ctx, actor, Job{ID: "job", Name: "Output", Script: "echo test", HostIDs: []string{"host"}, ExecutionMode: "sequential"}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitRun(ctx, actor, RunAdmission{ID: "run", RequestID: "run", JobID: job.ID, Version: job.Version}); err != nil {
		t.Fatal(err)
	}
	changes, stop := db.WatchRun("run")
	defer stop()
	state, stopState := db.WatchRunState("run")
	defer stopState()
	reject := `CREATE TRIGGER reject_output BEFORE INSERT ON run_output
        WHEN (SELECT COUNT(*) FROM run_output WHERE run_id = NEW.run_id) = 1
        BEGIN SELECT RAISE(ABORT, 'fixture rejected second chunk'); END`
	remove := "DROP TRIGGER reject_output"
	if db.db.dialect != "sqlite" {
		reject = "ALTER TABLE run_output ADD CONSTRAINT reject_output CHECK (OCTET_LENGTH(data) <> 1)"
		remove = "ALTER TABLE run_output DROP CONSTRAINT reject_output"
		if db.db.dialect == "mysql" {
			remove = "ALTER TABLE run_output DROP CHECK reject_output"
		}
	}
	if _, err := db.db.DB.ExecContext(ctx, reject); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), 4096*2+1)
	if err := db.AppendOutput(ctx, "run", "host", payload); err == nil {
		t.Fatal("fixture did not reject the batch")
	}
	chunks, err := db.RunOutput(ctx, actor, "run", "host", 0)
	if err != nil || len(chunks) != 0 || len(changes) != 0 || len(state) != 0 {
		t.Fatal("failed batch leaked rows or notifications")
	}
	if _, err := db.db.DB.ExecContext(ctx, remove); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendOutput(ctx, "run", "host", payload); err != nil {
		t.Fatal(err)
	}
	chunks, err = db.RunOutput(ctx, actor, "run", "host", 0)
	if err != nil || len(chunks) != 3 || len(changes) != 1 || len(state) != 0 {
		t.Fatal("committed batch was not bounded or notified correctly")
	}
	var restored []byte
	for _, chunk := range chunks {
		if len(chunk.Data) > 4096 {
			t.Fatal("persisted chunk exceeded history cursor bound")
		}
		restored = append(restored, chunk.Data...)
	}
	if !bytes.Equal(restored, payload) {
		t.Fatal("batch changed persisted output")
	}
	<-changes
	if err := db.AppendOutput(ctx, "run", "host", make([]byte, MaxOutputBatchBytes+1)); err == nil {
		t.Fatal("unbounded transaction accepted")
	}
	if err := db.AppendOutput(ctx, "run", "host", nil); err != nil || len(changes) != 0 {
		t.Fatal("empty output created a commit notification")
	}
}
