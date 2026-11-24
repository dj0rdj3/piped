package hosts

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"piped/internal/credentials"
	"piped/internal/secrets"
	"piped/internal/store"
)

func hostHarness(t *testing.T) (*Service, *store.Store, store.Actor, *secrets.Vault, string) {
	t.Helper()
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "data", "piped.db")
	db, err := store.Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := db.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, user, "session", time.Now().Unix(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{User: user, SessionHash: "session"}
	vault, _ := secrets.New(bytes.Repeat([]byte{23}, 32))
	service, err := New(ctx, db, vault)
	if err != nil {
		t.Fatal(err)
	}
	return service, db, actor, vault, filename
}
func textPointer(value string) *string { return &value }
func validInput() Input {
	return Input{Name: "Server", Address: "example.com", Port: 22, Username: "deploy", Password: textPointer("ssh-secret-test"),
		RunAsSudo: true, SudoPassword: textPointer("sudo-secret-test"), Tags: []string{"linux", "prod", "linux"}}
}

func TestEncryptedHostLifecycle(t *testing.T) {
	service, db, actor, vault, filename := hostHarness(t)
	ctx := context.Background()
	input := validInput()
	host, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	if !host.HasPassword || !host.HasSudoPassword || host.Version != 1 || !slices.Equal(host.Tags, []string{"linux", "prod"}) {
		t.Fatalf("unexpected host: %+v", host)
	}
	// Query only the disposable fixture to verify encryption, not to create records.
	inspection, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	var encryptedPassword, encryptedSudo []byte
	if err := inspection.QueryRow("SELECT encrypted_password, encrypted_sudo_password FROM hosts WHERE id = ?", host.ID).Scan(&encryptedPassword, &encryptedSudo); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encryptedPassword, []byte(*input.Password)) || bytes.Contains(encryptedSudo, []byte(*input.SudoPassword)) {
		t.Fatal("plaintext stored")
	}
	for _, secret := range []struct {
		encrypted      []byte
		purpose, value string
	}{
		{encryptedPassword, "password", *input.Password}, {encryptedSudo, "sudo-password", *input.SudoPassword},
	} {
		plain, err := vault.Open("host:"+host.ID+":"+secret.purpose, secret.encrypted)
		if err != nil || string(plain) != secret.value {
			t.Fatal("encrypted secret cannot be used")
		}
		clear(plain)
		if _, err := vault.Open("host:another:"+secret.purpose, secret.encrypted); !errors.Is(err, secrets.ErrDecrypt) {
			t.Fatal("ciphertext not bound to host")
		}
	}
	input.Version = host.Version
	input.Password, input.SudoPassword = nil, nil
	input.Address = "2001:db8::1"
	host, err = service.Save(ctx, actor, host.ID, input)
	if err != nil || host.Version != 2 || !host.HasPassword || !host.HasSudoPassword {
		t.Fatalf("secret preservation: %+v %v", host, err)
	}
	var preserved []byte
	if err := inspection.QueryRow("SELECT encrypted_password FROM hosts WHERE id = ?", host.ID).Scan(&preserved); err != nil || !bytes.Equal(preserved, encryptedPassword) {
		t.Fatal("edit changed stored password")
	}
	if _, err := service.Save(ctx, actor, host.ID, input); !errors.Is(err, store.ErrHostChanged) {
		t.Fatal("stale update accepted")
	}
	if err := service.Delete(ctx, actor, host.ID, 1); !errors.Is(err, store.ErrHostChanged) {
		t.Fatal("stale delete accepted")
	}
	input.Version = host.Version
	input.SudoPassword = textPointer("")
	host, err = service.Save(ctx, actor, host.ID, input)
	if err != nil || host.HasSudoPassword {
		t.Fatal("sudo password not cleared")
	}
	input.Version = host.Version
	input.SudoPassword = textPointer("replacement-sudo")
	host, err = service.Save(ctx, actor, host.ID, input)
	if err != nil || !host.HasSudoPassword {
		t.Fatal("sudo password not replaced")
	}
	input.Version, input.RunAsSudo, input.SudoPassword = host.Version, false, nil
	host, err = service.Save(ctx, actor, host.ID, input)
	if err != nil || host.HasSudoPassword || host.RunAsSudo {
		t.Fatal("disabled sudo retained secret")
	}
	if err := db.DeleteSession(ctx, actor.SessionHash); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(ctx, actor); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("revoked session read accepted")
	}
	input.Version = host.Version
	if _, err := service.Save(ctx, actor, host.ID, input); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("revoked session update accepted")
	}
	if _, err := service.Save(ctx, actor, "", validInput()); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("revoked session create accepted")
	}
	if err := service.Delete(ctx, actor, host.ID, host.Version); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("revoked session delete accepted")
	}
}

func TestHostKeyCredentialReferencesAndAuthenticationSwitch(t *testing.T) {
	service, db, actor, vault, _ := hostHarness(t)
	ctx := context.Background()
	credentialService, err := credentials.New(ctx, db, vault)
	if err != nil {
		t.Fatal(err)
	}
	key, err := credentialService.Create(ctx, actor, credentials.CreateInput{Name: "Deploy key", Mode: "generate"})
	if err != nil {
		t.Fatal(err)
	}
	input := validInput()
	host, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Version, input.Password, input.CredentialID = host.Version, nil, key.ID
	host, err = service.Save(ctx, actor, host.ID, input)
	if err != nil || host.HasPassword || host.CredentialName != key.Name {
		t.Fatalf("key switch: %+v %v", host, err)
	}
	if err := credentialService.Delete(ctx, actor, key.ID); !errors.Is(err, store.ErrCredentialInUse) {
		t.Fatal("assigned credential deleted")
	}
	if err := credentialService.Rename(ctx, actor, key.ID, "Renamed key"); err != nil {
		t.Fatal(err)
	}
	list, err := service.List(ctx, actor)
	if err != nil || len(list) != 1 || list[0].CredentialName != "Renamed key" {
		t.Fatal("credential rename not reflected")
	}
	input.Version, input.CredentialID = host.Version, ""
	if _, err := service.Save(ctx, actor, host.ID, input); !errors.Is(err, store.ErrHostPassword) {
		t.Fatal("password required when switching from key")
	}
	input.Password = textPointer("new-host-password")
	host, err = service.Save(ctx, actor, host.ID, input)
	if err != nil || !host.HasPassword || host.CredentialID != "" {
		t.Fatal("password switch failed")
	}
	if err := credentialService.Delete(ctx, actor, key.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, actor, host.ID, host.Version); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, actor, host.ID, host.Version); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("missing delete accepted")
	}
	input.Version = 0
	input.CredentialID, input.Password = key.ID, nil
	if _, err := service.Save(ctx, actor, "", input); !errors.Is(err, store.ErrHostCredential) {
		t.Fatal("missing credential accepted")
	}
}

func TestHostValidation(t *testing.T) {
	for _, address := range []string{"server", "server.example.com.", "192.0.2.1", "::1", "2001:db8::1", "xn--bcher-kva.example"} {
		input := validInput()
		input.Address = address
		if _, err := validate(input); err != nil {
			t.Fatalf("valid address %s: %v", address, err)
		}
	}
	for _, address := range []string{"", "https://example.com", "host:22", "example.com/path", "a b", "[::1]", "fe80::1%eth0", "-host", "host-", "a..com", "999.999.999.999", strings.Repeat("x", 64) + ".com"} {
		input := validInput()
		input.Address = address
		if _, err := validate(input); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid address accepted: %q", address)
		}
	}
	cases := []func(*Input){
		func(i *Input) { i.Name = "" }, func(i *Input) { i.Name = "bad\x00name" },
		func(i *Input) { i.Port = 0 }, func(i *Input) { i.Port = 65536 },
		func(i *Input) { i.Username = "bad user" }, func(i *Input) { i.Username = "" },
		func(i *Input) { i.Password = textPointer("") }, func(i *Input) { i.Password = textPointer(strings.Repeat("s", 1025)) },
		func(i *Input) { i.SudoPassword = textPointer("\x00") },
		func(i *Input) { i.RunAsSudo = false }, func(i *Input) { i.CredentialID = "bad" },
		func(i *Input) { i.CredentialID = strings.Repeat("a", 32) },
		func(i *Input) { i.Tags = []string{""} }, func(i *Input) { i.Tags = []string{"a,b"} },
		func(i *Input) { i.Tags = []string{"bad\nline"} }, func(i *Input) { i.Tags = []string{strings.Repeat("x", 51)} },
		func(i *Input) { i.Tags = make([]string, 21) },
	}
	for n, change := range cases {
		input := validInput()
		change(&input)
		if _, err := validate(input); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid case %d accepted", n)
		}
	}
	input := validInput()
	input.Name, input.Tags = "\u0414\u043e\u043c", []string{"\u65e5\u672c"}
	if _, err := validate(input); err != nil {
		t.Fatalf("Unicode name and tags rejected: %v", err)
	}
}

func TestTagNormalizationPreservesInputAndLimits(t *testing.T) {
	for _, fixture := range []struct {
		input, want []string
	}{
		{nil, []string{}},
		{[]string{}, []string{}},
		{[]string{" prod ", "linux", "prod", " Linux ", "\u65e5\u672c", "linux"}, []string{"Linux", "linux", "prod", "\u65e5\u672c"}},
		{strings.Fields(strings.Repeat("same ", 20)), []string{"same"}},
	} {
		input := validInput()
		input.Tags = fixture.input
		before := slices.Clone(input.Tags)
		host, err := validate(input)
		if err != nil || !slices.Equal(host.Tags, fixture.want) || host.Tags == nil {
			t.Fatalf("normalization: got %q, want %q: %v", host.Tags, fixture.want, err)
		}
		if !slices.Equal(input.Tags, before) {
			t.Fatal("normalization modified the caller's tag slice")
		}
	}
	input := validInput()
	input.Tags = strings.Fields(strings.Repeat("same ", 21))
	if _, err := validate(input); !errors.Is(err, ErrInput) {
		t.Fatal("deduplication bypassed the input tag limit")
	}
	input.Name = strings.Repeat("\u65e5", 100)
	input.Tags = []string{strings.Repeat("\u65e5", 50)}
	if _, err := validate(input); err != nil {
		t.Fatalf("Unicode character limits treated as byte limits: %v", err)
	}
	input.Name += "\u65e5"
	if _, err := validate(input); !errors.Is(err, ErrInput) {
		t.Fatal("Unicode name exceeded the character limit")
	}
}
