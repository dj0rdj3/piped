package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"piped/internal/auth"
	"piped/internal/store"
)

type scriptedPrompter struct {
	lines  []string
	secret []bool
}

func (p *scriptedPrompter) ReadLine(_ string, secret bool) (string, error) {
	p.secret = append(p.secret, secret)
	if len(p.lines) == 0 {
		return "", io.EOF
	}
	value := p.lines[0]
	p.lines = p.lines[1:]
	return value, nil
}

func registrationService(t *testing.T) *auth.Service {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data", "piped.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	service, err := auth.New(db)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestInteractiveRegistration(t *testing.T) {
	for _, username := range []string{"", " Admin.Team "} {
		t.Run(username, func(t *testing.T) {
			service := registrationService(t)
			password := "  a long and unique passphrase  "
			if username == "" {
				password = "pass1234"
			}
			prompts := &scriptedPrompter{lines: []string{username, password, password}}
			var output bytes.Buffer
			if err := register(context.Background(), service, prompts, &output); err != nil {
				t.Fatal(err)
			}
			name := "admin"
			if username != "" {
				name = "admin.team"
			}
			if output.String() != "Created administrator: "+name+"\n" {
				t.Fatalf("output: %s", output.String())
			}
			if strings.Contains(output.String(), password) {
				t.Fatal("password appeared in output")
			}
			if len(prompts.secret) != 3 || prompts.secret[0] || !prompts.secret[1] || !prompts.secret[2] {
				t.Fatalf("incorrect secret prompts: %v", prompts.secret)
			}
			if _, _, err := service.Login(context.Background(), name, password); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRegistrationRetriesInvalidInput(t *testing.T) {
	service := registrationService(t)
	password := "a long unique admin passphrase"
	prompts := &scriptedPrompter{lines: []string{"bad name", "admin", "short", password, "does not match", password, password}}
	var output bytes.Buffer
	if err := register(context.Background(), service, prompts, &output); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"username must", "password must", "Passwords do not match", "Created administrator"} {
		if !strings.Contains(output.String(), message) {
			t.Errorf("missing retry message %q", message)
		}
	}
	for _, secret := range []string{password, "does not match", "short"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("password appeared in output")
		}
	}
}

func TestRegistrationEOFDoesNotCreateAccount(t *testing.T) {
	for _, lines := range [][]string{nil, {"admin"}, {"admin", "a long unique passphrase"}} {
		service := registrationService(t)
		err := register(context.Background(), service, &scriptedPrompter{lines: lines}, &bytes.Buffer{})
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected EOF, got %v", err)
		}
		initialized, err := service.Store.HasUsers(context.Background())
		if err != nil || initialized {
			t.Fatalf("incomplete registration created account: %v", err)
		}
	}
}

func TestRegistrationPreservesExistingAdministrator(t *testing.T) {
	service := registrationService(t)
	if _, err := service.InitAdmin(context.Background(), "admin", "a long unique passphrase"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := register(context.Background(), service, &scriptedPrompter{}, &output)
	// The transactional insert still enforces one-time bootstrap if another process
	// creates an administrator while this command is collecting its input.
	if !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected prompt error: %v", err)
	}
	password := "another long unique passphrase"
	err = register(context.Background(), service, &scriptedPrompter{lines: []string{"other", password, password}}, &output)
	if !errors.Is(err, store.ErrInitialized) {
		t.Fatalf("registration replaced existing administrator: %v", err)
	}
}
