package store

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"piped/internal/secrets"
)

func TestCredentialTransactionsAndEncryptionBinding(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	v, _ := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err := s.VerifyVault(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyVault(ctx, v); err != nil {
		t.Fatal(err)
	}
	wrong, _ := secrets.New(bytes.Repeat([]byte{8}, 32))
	if err := s.VerifyVault(ctx, wrong); !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatal("wrong deployment key accepted")
	}
	u, err := s.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, s, u, "session")
	c := Credential{ID: "record1", Name: "Production", PublicKey: "public", Fingerprint: "fingerprint", Algorithm: "ssh-ed25519"}
	encrypted := v.Seal("credential:record1:secret", []byte("private material"))
	if _, err := s.CreateCredential(ctx, actor, c, encrypted); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := s.db.QueryRow("SELECT encrypted_secret FROM credentials WHERE id = ?", c.ID).Scan(&stored); err != nil || !bytes.Equal(stored, encrypted) {
		t.Fatalf("stored secret: %v", err)
	}
	c.ID, c.Name = "record2", "production"
	if _, err := s.CreateCredential(ctx, actor, c, encrypted); !errors.Is(err, ErrCredentialName) {
		t.Fatal("duplicate name accepted")
	}
	c.Name = "Other"
	if _, err := s.CreateCredential(ctx, actor, c, encrypted); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameCredential(ctx, actor, "record2", "PRODUCTION"); !errors.Is(err, ErrCredentialName) {
		t.Fatal("rename collision accepted")
	}
	if err := s.RenameCredential(ctx, actor, "missing", "Name"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing rename accepted")
	}
	if err := s.DeleteCredential(ctx, actor, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing delete accepted")
	}
	if err := s.RenameCredential(ctx, actor, "record1", "Renamed"); err != nil {
		t.Fatal(err)
	}
	items, err := s.Credentials(ctx, actor)
	if err != nil || len(items) != 2 || items[1].Name != "Renamed" {
		t.Fatalf("list: %+v %v", items, err)
	}
	if err := s.DeleteSession(ctx, actor.SessionHash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCredential(ctx, actor, c, encrypted); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked create accepted")
	}
	if _, err := s.Credentials(ctx, actor); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked list accepted")
	}
	if err := s.RenameCredential(ctx, actor, "record1", "Forbidden"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked rename accepted")
	}
	if err := s.DeleteCredential(ctx, actor, "record1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked delete accepted")
	}
	actor = actorFor(t, s, u, "new-session")
	if err := s.ChangePassword(ctx, actor, u.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCredential(ctx, actor, "record1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old password actor accepted")
	}
	if _, err := s.db.Exec("DELETE FROM encryption_settings"); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyVault(ctx, wrong); !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatal("missing canary reset existing secrets")
	}
}

func TestConcurrentCredentialNames(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, _ := s.InitAdmin(ctx, "admin", "hash")
	actor := actorFor(t, s, u, "session")
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := s.CreateCredential(ctx, actor, Credential{ID: id, Name: "same"}, []byte("encrypted"))
			errs <- err
		}(id)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrCredentialName) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("race: %d success, %d conflict", success, conflict)
	}
}

func TestMissingVaultVerificationRejectsEverySecretTable(t *testing.T) {
	for _, kind := range []string{"empty", "credential", "host", "job secret"} {
		t.Run(kind, func(t *testing.T) {
			db := openTest(t)
			ctx := context.Background()
			user, err := db.InitAdmin(ctx, "admin", "hash")
			if err != nil {
				t.Fatal(err)
			}
			actor := actorFor(t, db, user, "session")
			switch kind {
			case "credential":
				_, err = db.CreateCredential(ctx, actor, Credential{ID: "credential", Name: "Credential"}, []byte("encrypted key"))
			case "host":
				_, err = db.SaveHost(ctx, actor, Host{ID: "host", Name: "Host", Address: "localhost", Port: 22, Username: "deploy", Tags: []string{}}, []byte("encrypted password"), nil, true)
			case "job secret":
				_, err = db.SaveJob(ctx, actor, Job{ID: "job", Name: "Job", Script: "echo fixture", ExecutionMode: "sequential"}, []JobSecret{{Name: "TOKEN", Ciphertext: []byte("encrypted token")}}, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			vault, err := secrets.New(bytes.Repeat([]byte{19}, 32))
			if err != nil {
				t.Fatal(err)
			}
			err = db.VerifyVault(ctx, vault)
			wantRecords := 0
			if kind == "empty" {
				if err != nil {
					t.Fatal(err)
				}
				wantRecords = 1
			} else if !errors.Is(err, secrets.ErrDecrypt) {
				t.Fatalf("missing vault verification accepted %s: %v", kind, err)
			}
			var records int
			if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM encryption_settings`).Scan(&records); err != nil || records != wantRecords {
				t.Fatalf("unexpected verification records: %d, %v", records, err)
			}
		})
	}
}
