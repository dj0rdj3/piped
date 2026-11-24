//go:build development

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"piped/internal/auth"
	"piped/internal/store"
)

func TestAPIKeyCommandDefaultsAndValidation(t *testing.T) {
	database := filepath.Join(t.TempDir(), "data", "piped.db")
	var output bytes.Buffer
	if err := run([]string{"init-admin", "--db", database, "--username", "admin", "--password-stdin"}, strings.NewReader("test-admin-password\n"), &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"api-key", "--db", database, "--username", "admin"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(output.String())
	if !strings.HasPrefix(token, auth.APIKeyPrefix) || strings.Count(output.String(), "\n") != 1 {
		t.Fatal("CLI output should contain only the new token")
	}
	db, err := store.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	actor, err := db.ResolveAPIKey(context.Background(), auth.TokenHash(token), time.Now().Unix())
	if err != nil || !store.HasScope(actor.Scopes, store.ScopeUsersRead) || !store.HasScope(actor.Scopes, store.ScopeCredentialsRead) || store.HasScope(actor.Scopes, store.ScopeUsersWrite) || store.HasScope(actor.Scopes, store.ScopeCredentialsWrite) {
		t.Fatal("CLI default key is not read only")
	}
	for _, extra := range [][]string{
		{}, {"--username", "missing"}, {"--username", "admin", "--ttl", "0h"},
		{"--username", "admin", "--ttl", "2161h"}, {"--username", "admin", "--scopes", "*"},
	} {
		output.Reset()
		args := append([]string{"api-key", "--db", database}, extra...)
		if err := run(args, strings.NewReader(""), &output); err == nil || output.Len() != 0 {
			t.Fatalf("invalid command issued a token: %v", extra)
		}
	}
}

func TestAPIKeyAutomationCanIssueAndRevokeWithoutBrowser(t *testing.T) {
	database := filepath.Join(t.TempDir(), "data", "piped.db")
	var output bytes.Buffer
	if err := run([]string{"init-admin", "--db", database, "--username", "agent", "--password-stdin"}, strings.NewReader("test-admin-password\n"), &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"api-key", "--db", database, "--username", "agent", "--ttl", "1h", "--scopes", store.ScopeCredentialsRead, "--json"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	var issued struct {
		Key   store.APIKey `json:"key"`
		Token string       `json:"token"`
	}
	if err := json.Unmarshal(output.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Key.ID == "" || issued.Token == "" || issued.Key.ExpiresAt-issued.Key.CreatedAt != 3600 {
		t.Fatal("invalid automation output")
	}
	db, err := store.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	actor, err := db.ResolveAPIKey(context.Background(), auth.TokenHash(issued.Token), time.Now().Unix())
	if err != nil || len(actor.Scopes) != 1 || actor.Scopes[0] != store.ScopeCredentialsRead {
		t.Fatal("automatic key has incorrect scope")
	}
	output.Reset()
	if err := run([]string{"api-key", "--db", database, "--revoke", issued.Key.ID, "--json"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "{\"revoked\":true}\n" {
		t.Fatal("invalid revocation output")
	}
	if _, err := db.ResolveAPIKey(context.Background(), auth.TokenHash(issued.Token), time.Now().Unix()); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("CLI revocation did not invalidate key")
	}
	for _, id := range []string{"invalid", strings.Repeat("b", 32)} {
		output.Reset()
		if err := run([]string{"api-key", "--db", database, "--revoke", id}, strings.NewReader(""), &output); err == nil || output.Len() != 0 {
			t.Fatal("invalid revocation succeeded")
		}
	}
}

type capturedFailedOutput struct{ data []byte }

func (w *capturedFailedOutput) Write(data []byte) (int, error) {
	w.data = append(w.data, data...)
	return 0, io.ErrClosedPipe
}

func TestAPIKeyFailedOutputRevokesCommittedKey(t *testing.T) {
	database := filepath.Join(t.TempDir(), "data", "piped.db")
	if err := run([]string{"init-admin", "--db", database, "--password-stdin"}, strings.NewReader("fixture-password\n"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	output := &capturedFailedOutput{}
	err := run([]string{"api-key", "--db", database, "--username", "admin", "--ttl", "1h", "--json"}, strings.NewReader(""), output)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("key output failure was hidden: %v", err)
	}
	defer clear(output.data)
	var issued struct {
		Key   store.APIKey `json:"key"`
		Token string       `json:"token"`
	}
	if err := json.Unmarshal(output.data, &issued); err != nil || issued.Key.ID == "" || issued.Token == "" {
		t.Fatal("failed-output fixture did not observe a committed key")
	}
	db, err := store.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ResolveAPIKey(context.Background(), auth.TokenHash(issued.Token), time.Now().Unix()); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("failed output left the new key usable")
	}
}

func TestInitAdminOutputFailureDoesNotHideOrUndoCreation(t *testing.T) {
	database := filepath.Join(t.TempDir(), "data", "piped.db")
	err := run([]string{"init-admin", "--db", database, "--password-stdin"}, strings.NewReader("fixture-password\n"), failedOutput{io.ErrClosedPipe})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("administrator output failure was hidden: %v", err)
	}
	db, err := store.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, err := auth.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Login(context.Background(), "admin", "fixture-password"); err != nil {
		t.Fatal("committed administrator was lost after output failure")
	}
}
