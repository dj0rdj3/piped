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
const UserAPIKeyPrefix = "piped_key_"

var ErrAPIKeyInput = errors.New("invalid API key settings")

func NewAPIKey(user store.User, name string, scopes []string, lifetime time.Duration) (store.APIKey, string, error) {
	if lifetime == 0 {
		return store.APIKey{}, "", ErrAPIKeyInput
	}
	return newAPIKey(user, name, scopes, lifetime, APIKeyPrefix)
}

func NewUserAPIKey(user store.User, name string, scopes []string, lifetime time.Duration) (store.APIKey, string, error) {
	key, token, err := newAPIKey(user, name, scopes, lifetime, UserAPIKeyPrefix)
	key.Purpose = "production"
	return key, token, err
}

func APIKeyScopes() []string {
	return []string{
		store.ScopeUsersRead, store.ScopeUsersWrite,
		store.ScopeCredentialsRead, store.ScopeCredentialsWrite,
		store.ScopeHostsRead, store.ScopeHostsWrite,
		store.ScopeJobsRead, store.ScopeJobsWrite,
		store.ScopeRunsRead, store.ScopeRunsWrite,
		store.ScopeNotificationsRead, store.ScopeNotificationsWrite,
	}
}

func newAPIKey(user store.User, name string, scopes []string, lifetime time.Duration, prefix string) (store.APIKey, string, error) {
	name = strings.TrimSpace(name)
	if lifetime != 0 && (lifetime < time.Hour || lifetime > 90*24*time.Hour) {
		return store.APIKey{}, "", ErrAPIKeyInput
	}
	if !utf8.ValidString(name) || len(name) == 0 || utf8.RuneCountInString(name) > 100 {
		return store.APIKey{}, "", ErrAPIKeyInput
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return store.APIKey{}, "", ErrAPIKeyInput
		}
	}
	allowed := APIKeyScopes()
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
	token := prefix + base64.RawURLEncoding.EncodeToString(randomBytes())
	now := time.Now()
	key := store.APIKey{
		ID:        hex.EncodeToString(id),
		Name:      name,
		UserID:    user.ID,
		Username:  user.Username,
		Scopes:    scopes,
		CreatedAt: now.Unix(),
	}
	if lifetime > 0 {
		key.ExpiresAt = now.Add(lifetime).Unix()
	}
	return key, token, nil
}

// CreateUserAPIKey returns the one-time token only after its digest is committed
// under the owner's fresh browser-session authority.
func (s *Service) CreateUserAPIKey(ctx context.Context, actor store.Actor, name string, scopes []string, lifetime time.Duration) (store.APIKey, string, error) {
	key, token, err := NewUserAPIKey(actor.User, name, scopes, lifetime)
	if err != nil {
		return store.APIKey{}, "", err
	}
	if err := s.Store.CreateUserAPIKey(ctx, actor, key, TokenHash(token)); err != nil {
		return store.APIKey{}, "", err
	}
	return key, token, nil
}

func (s *Service) ResolveAPIKey(ctx context.Context, token string) (store.Actor, error) {
	return s.resolveAPIKey(ctx, token, APIKeyPrefix, "development")
}

// ResolveUserAPIKey never accepts internal development keys, even on loopback.
func (s *Service) ResolveUserAPIKey(ctx context.Context, token string) (store.Actor, error) {
	return s.resolveAPIKey(ctx, token, UserAPIKeyPrefix, "production")
}

func (s *Service) resolveAPIKey(ctx context.Context, token, prefix, purpose string) (store.Actor, error) {
	if !strings.HasPrefix(token, prefix) || len(token) != len(prefix)+43 {
		return store.Actor{}, store.ErrUnauthorized
	}
	actor, err := s.Store.ResolveAPIKey(ctx, TokenHash(token), time.Now().Unix())
	if err != nil {
		return store.Actor{}, err
	}
	if actor.APIKeyPurpose != purpose {
		return store.Actor{}, store.ErrUnauthorized
	}
	return actor, nil
}
