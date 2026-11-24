//go:build development

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"piped/internal/auth"
	"piped/internal/store"
)

func TestDevelopmentOverridesAreTemporary(t *testing.T) {
	path := configurationFixture(t, "PIPED_DATABASE_PATH=private/production.db\n"+
		"PIPED_LISTEN=0.0.0.0:9090\nPIPED_PUBLIC_URL=https://piped.example\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	options, err := parseOptions([]string{
		"--env-file", path, "--development", "--db", "test/piped.db",
		"--listen", "127.0.0.1:8081", "--public-url", "http://127.0.0.1:5173",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := loadConfiguration(options)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.DatabasePath != "test/piped.db" || configuration.ListenAddress != "127.0.0.1:8081" || configuration.PublicURL != "http://127.0.0.1:5173" {
		t.Fatal("development overrides were not applied")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("temporary overrides changed the deployment configuration")
	}
	for _, flag := range []string{"--db", "--listen", "--public-url", "--env-file"} {
		if _, err := parseOptions([]string{flag, ""}, &bytes.Buffer{}); err == nil {
			t.Fatalf("empty override accepted: %s", flag)
		}
	}
}

func TestDevelopmentOverridesKeepLoopbackRestrictions(t *testing.T) {
	path := configurationFixture(t, "PIPED_LISTEN=0.0.0.0:9090\nPIPED_PUBLIC_URL=https://piped.example\n")
	options, err := parseOptions([]string{"--env-file", path, "--development"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfiguration(options); err == nil {
		t.Fatal("development integrations accepted network-facing deployment settings")
	}
}

func TestDevelopmentSetupUsesEnvironmentDatabase(t *testing.T) {
	t.Chdir(t.TempDir())
	database := filepath.Join("configured", "piped.db")
	if err := os.WriteFile(".env", []byte("PIPED_DATABASE_PATH="+database+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"init-admin", "--username", "agent", "--password-stdin"}, strings.NewReader("test-password\n"), &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"api-key", "--username", "agent", "--ttl", "1h", "--scopes", store.ScopeUsersRead, "--json"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	var issued struct {
		Key   store.APIKey `json:"key"`
		Token string       `json:"token"`
	}
	if err := json.Unmarshal(output.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run([]string{"api-key", "--revoke", issued.Key.ID, "--json"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
			t.Error(err)
		}
	})
	db, err := store.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	actor, err := db.ResolveAPIKey(context.Background(), auth.TokenHash(issued.Token), time.Now().Unix())
	if err != nil || actor.Username != "agent" || len(actor.Scopes) != 1 || actor.Scopes[0] != store.ScopeUsersRead {
		t.Fatal("setup commands did not share the configured database")
	}
	if _, err := os.Stat(filepath.Join("data", "piped.db")); !os.IsNotExist(err) {
		t.Fatal("setup also opened the default database")
	}
}
