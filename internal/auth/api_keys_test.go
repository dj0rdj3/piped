package auth

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"piped/internal/store"
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
