package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	ScopeJobsRead         = "jobs:read"
	ScopeJobsWrite        = "jobs:write"
	ScopeHostsRead        = "hosts:read"
	ScopeHostsWrite       = "hosts:write"
	ScopeUsersRead        = "users:read"
	ScopeUsersWrite       = "users:write"
	ScopeCredentialsRead  = "credentials:read"
	ScopeCredentialsWrite = "credentials:write"
)

func HasScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

type APIKey struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	UserID    int64    `json:"user_id"`
	Username  string   `json:"username"`
	Scopes    []string `json:"scopes"`
	CreatedAt int64    `json:"created_at"`
	ExpiresAt int64    `json:"expires_at"`
}

// ResolveAPIKey never selects or returns a raw token: only a SHA-256 digest is
// persisted. Ownership, expiry, and scopes are rechecked in write transactions.
func (s *Store) ResolveAPIKey(ctx context.Context, hash string, now int64) (Actor, error) {
	var actor Actor
	var scopes string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.password_hash, u.created_at, k.scopes
		FROM api_keys k JOIN users u ON u.id = k.user_id
		WHERE k.token_hash = ? AND k.expires_at > ?
	`, hash, now).Scan(&actor.ID, &actor.Username, &actor.PasswordHash, &actor.CreatedAt, &scopes)
	if errors.Is(err, sql.ErrNoRows) {
		return Actor{}, ErrUnauthorized
	}
	if err != nil {
		return Actor{}, err
	}
	actor.APIKeyHash = hash
	actor.Scopes = strings.Split(scopes, ",")
	return actor, nil
}

// CreateLocalAPIKey is exclusively for the CLI, whose authority is access to the
// configured database credentials or private SQLite file. It still checks the owner.
func (s *Store) CreateLocalAPIKey(ctx context.Context, user User, key APIKey, hash string) error {
	if key.UserID != user.ID {
		return ErrUnauthorized
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO api_keys(id, user_id, name, token_hash, scopes, created_at, expires_at)
		SELECT ?, id, ?, ?, ?, ?, ? FROM users WHERE id = ? AND password_hash = ?
	`, key.ID, key.Name, hash, strings.Join(key.Scopes, ","), key.CreatedAt, key.ExpiresAt, user.ID, user.PasswordHash)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrUnauthorized
	}
	return nil
}

// RevokeLocalAPIKey is CLI-only; authority comes from access to the private
// database configuration or SQLite file. No HTTP endpoint manages API keys.
func (s *Store) RevokeLocalAPIKey(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM api_keys WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) PurgeAPIKeys(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM api_keys WHERE expires_at <= ?", now.Unix())
	return err
}
