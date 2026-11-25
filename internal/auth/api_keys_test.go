package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"piped/internal/store"
	"piped/internal/testdatabase"
)

func TestNewAPIKey(t *testing.T) {
	user := store.User{ID: 7, Username: "developer"}
	scopes := []string{store.ScopeUsersRead, store.ScopeCredentialsRead}
	original := append([]string(nil), scopes...)
	key, token, err := NewAPIKey(user, " Development ", scopes, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	random, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, APIKeyPrefix))
	if err != nil || len(random) != 32 || !strings.HasPrefix(token, APIKeyPrefix) {
		t.Fatal("invalid bearer token format")
	}
	if key.Name != "Development" || key.UserID != user.ID || key.Username != user.Username || len(key.ID) != 32 || key.ExpiresAt-key.CreatedAt != int64((7*24*time.Hour).Seconds()) {
		t.Fatal("invalid public key metadata")
	}
	if !reflect.DeepEqual(scopes, original) {
		t.Fatal("caller scope slice was mutated")
	}
	other, otherToken, err := NewAPIKey(user, "Other", scopes, time.Hour)
	if err != nil || other.ID == key.ID || otherToken == token {
		t.Fatal("API keys are not independently random")
	}
}

func TestAPIKeySettingsRejectInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scopes []string
		ttl    time.Duration
	}{
		{"", []string{store.ScopeUsersRead}, time.Hour},
		{"\xff", []string{store.ScopeUsersRead}, time.Hour},
		{"line\nbreak", []string{store.ScopeUsersRead}, time.Hour},
		{strings.Repeat("a", 101), []string{store.ScopeUsersRead}, time.Hour},
		{"Valid", nil, time.Hour},
		{"Valid", []string{"*"}, time.Hour},
		{"Valid", []string{store.ScopeUsersRead, store.ScopeUsersRead}, time.Hour},
		{"Valid", []string{store.ScopeUsersRead}, time.Hour - time.Second},
		{"Valid", []string{store.ScopeUsersRead}, 90*24*time.Hour + time.Second},
	} {
		_, token, err := NewAPIKey(store.User{ID: 1}, tc.name, tc.scopes, tc.ttl)
		if !errors.Is(err, ErrAPIKeyInput) || token != "" {
			t.Fatalf("accepted invalid settings: %+v", tc)
		}
	}
}

func TestNoExpirationIsAvailableOnlyForPersonalKeys(t *testing.T) {
	user := store.User{ID: 1, Username: "owner"}
	key, token, err := NewUserAPIKey(user, "No expiration", []string{store.ScopeUsersRead}, 0)
	if err != nil || token == "" || key.ExpiresAt != 0 || key.Purpose != "production" {
		t.Fatalf("no-expiration personal key rejected: %v", err)
	}
	if _, token, err := NewAPIKey(user, "Internal", []string{store.ScopeUsersRead}, 0); !errors.Is(err, ErrAPIKeyInput) || token != "" {
		t.Fatal("internal development key accepted no expiration")
	}
	for _, lifetime := range []time.Duration{-time.Hour, time.Second, 90*24*time.Hour + time.Second} {
		if _, token, err := NewUserAPIKey(user, "Invalid", []string{store.ScopeUsersRead}, lifetime); !errors.Is(err, ErrAPIKeyInput) || token != "" {
			t.Fatal("invalid personal key lifetime accepted")
		}
	}
}

func TestPersonalKeyIssuanceAndPurposeChecks(t *testing.T) {
	ctx := context.Background()
	driver, dsn := testdatabase.Settings(t)
	db, err := store.OpenDatabase(ctx, store.Config{Driver: driver, DSN: dsn, Path: filepath.Join(t.TempDir(), "data", "piped.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	owner, err := db.InitAdmin(ctx, "owner", "owner-hash")
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := db.CreateSession(ctx, owner, "owner-session", now, now+3600); err != nil {
		t.Fatal(err)
	}
	session := store.Actor{User: owner, SessionHash: "owner-session"}
	scopes := []string{store.ScopeUsersRead}
	key, token, err := service.CreateUserAPIKey(ctx, session, "Personal", scopes, 0)
	if err != nil || key.UserID != owner.ID || key.ExpiresAt != 0 {
		t.Fatalf("personal issuance failed: %v", err)
	}
	bearer, err := service.ResolveUserAPIKey(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveAPIKey(ctx, token); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("development lookup accepted a personal key")
	}
	if key, token, err := service.CreateUserAPIKey(ctx, bearer, "Forbidden", scopes, time.Hour); !errors.Is(err, store.ErrForbidden) || token != "" || key.ID != "" {
		t.Fatal("bearer issuance returned a key or token")
	}
	// Prefix validation cannot substitute for the persisted purpose check.
	wrongPurpose, wrongPurposeToken, err := NewUserAPIKey(owner, "Wrong purpose", scopes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateLocalAPIKey(ctx, owner, wrongPurpose, TokenHash(wrongPurposeToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveUserAPIKey(ctx, wrongPurposeToken); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("personal lookup ignored stored development purpose")
	}
	wrongPurpose, wrongPurposeToken, err = NewAPIKey(owner, "Wrong purpose", scopes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wrongPurpose.Purpose = "production"
	if err := db.CreateUserAPIKey(ctx, session, wrongPurpose, TokenHash(wrongPurposeToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveAPIKey(ctx, wrongPurposeToken); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("development lookup ignored stored production purpose")
	}
	if err := db.DeleteSession(ctx, session.SessionHash); err != nil {
		t.Fatal(err)
	}
	if key, token, err := service.CreateUserAPIKey(ctx, session, "Stale session", scopes, time.Hour); !errors.Is(err, store.ErrUnauthorized) || token != "" || key.ID != "" {
		t.Fatal("failed persistence returned a key or one-time token")
	}
}
