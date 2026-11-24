//go:build !development

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestProductionRejectsDevelopmentCommands(t *testing.T) {
	for _, args := range [][]string{
		{"--development"}, {"api-key", "--username", "admin"},
		{"init-admin", "--username", "admin", "--password-stdin"},
		{"--listen", "127.0.0.1:8081"}, {"--public-url", "https://piped.example"},
		{"--db", "other.db"}, {"--env-file", "other.env"},
		{"register", "--db", "other.db"}, {"keygen", "--env-file", "other.env"},
	} {
		if err := run(args, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Fatalf("production accepted development command: %v", args)
		}
	}
}

func TestProductionHelpHasNoRuntimeFlags(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"--help"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-db", "-listen", "-public-url", "-env-file", "-development", "init-admin", "api-key"} {
		if strings.Contains(output.String(), flag) {
			t.Fatalf("production help advertised %q", flag)
		}
	}
}
