package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"piped/internal/store"
)

const APIKeyPrefix = "piped_dev_"

var ErrAPIKeyInput = errors.New("invalid API key settings")

func NewAPIKey(user store.User, name string, scopes []string, lifetime time.Duration) (store.APIKey, string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || len(name) == 0 || utf8.RuneCountInString(name) > 100 || lifetime < time.Hour || lifetime > 90*24*time.Hour {
		return store.APIKey{}, "", ErrAPIKeyInput
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return store.APIKey{}, "", ErrAPIKeyInput
		}
	}
	allowed := []string{store.ScopeUsersRead, store.ScopeUsersWrite, store.ScopeCredentialsRead, store.ScopeCredentialsWrite, store.ScopeHostsRead, store.ScopeHostsWrite, store.ScopeJobsRead, store.ScopeJobsWrite, store.ScopeRunsRead, store.ScopeRunsWrite}
	if len(scopes) == 0 || len(scopes) > len(allowed) {
		return store.APIKey{}, "", ErrAPIKeyInput
	}
	scopes = slices.Clone(scopes)
	slices.Sort(scopes)
	for i, scope := range scopes {
		if !slices.Contains(allowed, scope) || i > 0 && scopes[i-1] == scope {
			return store.APIKey{}, "", ErrAPIKeyInput
		}
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	token := APIKeyPrefix + base64.RawURLEncoding.EncodeToString(randomBytes())
	now := time.Now()
	key := store.APIKey{
		ID:        hex.EncodeToString(id),
		Name:      name,
		UserID:    user.ID,
		Username:  user.Username,
		Scopes:    scopes,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(lifetime).Unix(),
	}
	return key, token, nil
}

func (s *Service) ResolveAPIKey(ctx context.Context, token string) (store.Actor, error) {
	if !strings.HasPrefix(token, APIKeyPrefix) || len(token) != len(APIKeyPrefix)+43 {
		return store.Actor{}, store.ErrUnauthorized
	}
	return s.Store.ResolveAPIKey(ctx, TokenHash(token), time.Now().Unix())
}
