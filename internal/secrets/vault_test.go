package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthenticatedEncryption(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	v, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	clear(key) // New retains its own expanded cipher key.
	plain := []byte("private key and passphrase")
	a, b := v.Seal("credential:1:secret", plain), v.Seal("credential:1:secret", plain)
	if bytes.Equal(a, b) || bytes.Contains(a, plain) {
		t.Fatal("non-random or exposed ciphertext")
	}
	got, err := v.Open("credential:1:secret", a)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: %q %v", got, err)
	}
	wrong, _ := New(make([]byte, 32))
	if _, err := wrong.Open("credential:1:secret", a); !errors.Is(err, ErrDecrypt) {
		t.Fatal("wrong key accepted")
	}
	for _, purpose := range []string{"credential:2:secret", "credential:1:password", ""} {
		if _, err := v.Open(purpose, a); !errors.Is(err, ErrDecrypt) {
			t.Fatal("wrong context accepted")
		}
	}
	for i := range a {
		tampered := bytes.Clone(a)
		tampered[i] ^= 1
		if _, err := v.Open("credential:1:secret", tampered); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("tampering accepted at byte %d", i)
		}
	}
	for i := 0; i < len(a); i++ {
		if _, err := v.Open("credential:1:secret", a[:i]); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("truncation accepted at %d", i)
		}
	}
	for _, size := range []int{0, 16, 24, 31, 33} {
		if _, err := New(make([]byte, size)); err == nil {
			t.Fatalf("accepted key length %d", size)
		}
	}
}

func TestEnvGenerationAndLoading(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := GenerateEnv(path); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("key file is not private")
	}
	v, err := LoadEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := GenerateEnv(path); err == nil {
		t.Fatal("overwrote existing key")
	}
	v2, err := LoadEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.Open("test", v.Seal("test", []byte("saved"))); err != nil {
		t.Fatal("key changed")
	}
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnv(link); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := GenerateEnv(link); err == nil {
		t.Fatal("overwrote symlink")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnv(path); err == nil {
		t.Fatal("accepted readable key file")
	}
	if _, err := LoadEnv(filepath.Dir(path)); err == nil {
		t.Fatal("accepted directory")
	}
	if _, err := LoadEnv(path + "missing"); err == nil {
		t.Fatal("accepted missing key")
	}
}

func TestEnvFormatAndSanitizedErrors(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{71}, 32))
	for _, body := range []string{"PIPED_ENCRYPTION_KEY=" + key + "\n", "# Deployment key\r\n\n PIPED_ENCRYPTION_KEY = '" + key + "'\r\n", "PIPED_ENCRYPTION_KEY=\"" + key + "\""} {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadEnv(path); err != nil {
			t.Fatalf("valid format rejected: %v", err)
		}
	}
	for _, body := range []string{"", "# empty", "PIPED_ENCRYPTION_KEY=", "PIPED_ENCRYPTION_KEY=secret-value", "PIPED_ENCRYPTION_KEY=" + key + "\nPIPED_ENCRYPTION_KEY=" + key, "OTHER=secret-value", "export PIPED_ENCRYPTION_KEY=" + key, "PIPED_ENCRYPTION_KEY=$(echo secret-value)", strings.Repeat("x", 4097), "PIPED_ENCRYPTION_KEY=" + base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadEnv(path)
		if err == nil {
			t.Fatal("invalid env accepted")
		}
		if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "secret-value") {
			t.Fatal("error exposes secret")
		}
	}
}

func TestExecutionRetentionConfiguration(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{71}, 32))
	for _, fixture := range []struct {
		setting string
		days    int
		valid   bool
	}{
		{"", 7, true}, {"PIPED_LOG_RETENTION_DAYS=\n", 7, true}, {"PIPED_LOG_RETENTION_DAYS=30\n", 30, true},
		{"PIPED_LOG_RETENTION_DAYS=1\n", 1, true}, {"PIPED_LOG_RETENTION_DAYS=36500\n", 36500, true},
		{"PIPED_LOG_RETENTION_DAYS=0", 0, false}, {"PIPED_LOG_RETENTION_DAYS=-1", 0, false},
		{"PIPED_LOG_RETENTION_DAYS=secret-value", 0, false}, {"PIPED_LOG_RETENTION_DAYS=36501", 0, false},
		{"PIPED_LOG_RETENTION_DAYS=7\nPIPED_LOG_RETENTION_DAYS=8", 0, false},
	} {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte("PIPED_ENCRYPTION_KEY="+key+"\n"+fixture.setting), 0600); err != nil {
			t.Fatal(err)
		}
		configuration, err := LoadConfiguration(path)
		if !fixture.valid {
			if err == nil {
				t.Fatal("invalid retention accepted")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("setting leaked")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if configuration.LogRetentionDays != fixture.days {
			t.Fatalf("retention=%d want=%d", configuration.LogRetentionDays, fixture.days)
		}
	}
}
