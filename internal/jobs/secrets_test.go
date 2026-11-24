package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"piped/internal/secrets"
	"piped/internal/store"
)

func secretValue(value string) *string { return &value }

func TestJobSecretLifecycleAndEncryption(t *testing.T) {
	service, db, actor, targets := jobHarness(t)
	ctx := context.Background()
	input := jobInput(targets)
	value := "sensitive-token-\"'$(not-executed)\nsecond line\n"
	input.Secrets = []SecretInput{{Name: "API_TOKEN", Value: &value}}
	job, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(job.SecretNames, []string{"API_TOKEN"}) {
		t.Fatalf("missing names: %+v", job.SecretNames)
	}
	listed, err := service.List(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(listed)
	if bytes.Contains(encoded, []byte("sensitive-token")) || bytes.Contains(encoded, []byte("encrypted_value")) {
		t.Fatal("secret returned by metadata read")
	}
	_, _, connections, err := db.CreateRun(ctx, actor, "snapshot", job.ID, job.Version)
	if err != nil {
		t.Fatal(err)
	}
	secret := connections[0].JobSecrets[0]
	if bytes.Contains(secret.Ciphertext, []byte(value)) {
		t.Fatal("secret stored in plaintext")
	}
	plain, err := service.vault.Open("job:"+job.ID+":env:API_TOKEN", secret.Ciphertext)
	if err != nil || string(plain) != value {
		t.Fatalf("stored value unavailable: %v", err)
	}
	clear(plain)
	for _, purpose := range []string{"job:other:env:API_TOKEN", "job:" + job.ID + ":env:OTHER"} {
		if _, err := service.vault.Open(purpose, secret.Ciphertext); !errors.Is(err, secrets.ErrDecrypt) {
			t.Fatal("ciphertext not bound to job and name")
		}
	}
	input.Version = job.Version
	input.Secrets = nil
	job, err = service.Save(ctx, actor, job.ID, input)
	if err != nil || len(job.SecretNames) != 1 {
		t.Fatal("omitted list did not preserve secrets")
	}
	input.Version = job.Version
	input.Secrets = []SecretInput{{Name: "API_TOKEN"}, {Name: "NEW_SECRET"}}
	if _, err := service.Save(ctx, actor, job.ID, input); !errors.Is(err, store.ErrJobSecret) {
		t.Fatalf("new name without value accepted: %v", err)
	}
	input.Secrets = []SecretInput{{Name: "API_TOKEN", Value: secretValue("replacement-token")}, {Name: "NEW_SECRET", Value: secretValue("another-token")}}
	updated, err := service.Save(ctx, actor, job.ID, input)
	if err != nil || updated.Version != job.Version+1 {
		t.Fatalf("replacement failed: %v", err)
	}
	if _, err := service.Save(ctx, actor, job.ID, input); !errors.Is(err, store.ErrJobChanged) {
		t.Fatalf("stale secret edit accepted: %v", err)
	}
	// Admission captured the old ciphertext even after values change or disappear.
	plain, err = service.vault.Open("job:"+job.ID+":env:API_TOKEN", connections[0].JobSecrets[0].Ciphertext)
	if err != nil || string(plain) != value {
		t.Fatal("active snapshot changed")
	}
	clear(plain)
	input.Version = updated.Version
	input.Secrets = []SecretInput{{Name: "API_TOKEN"}}
	updated, err = service.Save(ctx, actor, job.ID, input)
	if err != nil || !slices.Equal(updated.SecretNames, []string{"API_TOKEN"}) {
		t.Fatalf("removal/preservation failed: %v", err)
	}
	input.Version = updated.Version
	input.Secrets = []SecretInput{}
	updated, err = service.Save(ctx, actor, job.ID, input)
	if err != nil || len(updated.SecretNames) != 0 {
		t.Fatalf("clear failed: %v", err)
	}
	if err := service.Delete(ctx, actor, updated.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
}

func TestJobSecretValidationAndVault(t *testing.T) {
	for _, name := range []string{"", "1TOKEN", "lowercase", "A-B", "A=B", "A\x00B", "\u00c9", "PATH", "LD_PRELOAD", "BASH_ENV", "IFS", "ENV", "HOME", "PS4", "PROMPT_COMMAND", "SUDO_ASKPASS", "PIPED_INPUT", "BASH_FUNC_sudo%%", strings.Repeat("A", 65)} {
		if err := validateSecrets([]SecretInput{{Name: name, Value: secretValue("value")}}); !errors.Is(err, ErrInput) {
			t.Fatalf("unsafe name accepted: %q", name)
		}
	}
	for _, value := range []string{"", "bad\x00value", "\xff", strings.Repeat("a", MaxSecretBytes+1)} {
		if err := validateSecrets([]SecretInput{{Name: "TOKEN", Value: &value}}); !errors.Is(err, ErrInput) {
			t.Fatal("invalid value accepted")
		}
	}
	if err := validateSecrets([]SecretInput{{Name: "TOKEN"}, {Name: "TOKEN"}}); !errors.Is(err, ErrInput) {
		t.Fatal("duplicate accepted")
	}
	if err := validateSecrets(make([]SecretInput, MaxSecrets+1)); !errors.Is(err, ErrInput) {
		t.Fatal("too many secrets accepted")
	}
	if err := validateSecrets([]SecretInput{{Name: "_TOKEN_1", Value: secretValue("\u65e5\u672c\n\x03\r\n")}}); err != nil {
		t.Fatal(err)
	}
	_, db, _, _ := jobHarness(t)
	if _, err := New(context.Background(), db, nil); err == nil {
		t.Fatal("missing vault accepted")
	}
	wrong, _ := secrets.New(bytes.Repeat([]byte{99}, 32))
	if _, err := New(context.Background(), db, wrong); !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatal("wrong vault accepted")
	}
}
