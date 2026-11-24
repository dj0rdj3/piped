package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"piped/internal/auth"
	"piped/internal/store"
)

func TestCLIValidationAndConfiguredRegistration(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{"unknown"}, {"init-admin"}, {"serve", "extra"}, {"register", "extra"}} {
		if err := run(args, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	var help bytes.Buffer
	if err := run([]string{"--help"}, strings.NewReader(""), &help); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(help.String(), "register") || !strings.Contains(help.String(), "without a command") || !strings.Contains(help.String(), ".env") {
		t.Fatal("help does not explain registration and configured startup")
	}

	database := filepath.Join("data", "configured.db")
	if err := os.WriteFile(".env", []byte("PIPED_DATABASE_PATH="+database+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.New(db)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := service.InitAdmin(context.Background(), "admin", "correct horse battery staple"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"register"}, strings.NewReader(""), &output); !errors.Is(err, store.ErrInitialized) {
		t.Fatalf("registration did not use the configured database: %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("registration prompted after initialization")
	}
	if _, err := os.Stat(filepath.Join("data", "piped.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("registration also opened the default database")
	}
}

func TestKeygenAndRequiredDeploymentKey(t *testing.T) {
	t.Chdir(t.TempDir())
	var output bytes.Buffer
	if err := run(nil, strings.NewReader(""), &output); err == nil {
		t.Fatal("server started without encryption key")
	}
	if _, err := os.Stat(filepath.Join("data", "piped.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid encryption config mutated database")
	}
	if err := run([]string{"keygen"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(".env")
	if err != nil {
		t.Fatal(err)
	}
	keyLine := strings.SplitN(string(data), "\n", 2)[0]
	key := strings.TrimPrefix(keyLine, "PIPED_ENCRYPTION_KEY=")
	if strings.Contains(output.String(), key) {
		t.Fatal("keygen printed key")
	}
	if err := run([]string{"keygen"}, strings.NewReader(""), &output); err == nil {
		t.Fatal("keygen overwrote configuration")
	}
	after, err := os.ReadFile(".env")
	if err != nil || !bytes.Equal(after, data) {
		t.Fatal("existing configuration changed")
	}
}
