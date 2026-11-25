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
	Purpose   string   `json:"-"`
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	UserID    int64    `json:"user_id"`
	Username  string   `json:"username"`
	Scopes    []string `json:"scopes"`
	CreatedAt int64    `json:"created_at"`
	ExpiresAt int64    `json:"expires_at"` // Zero means no expiration for personal keys.
}

// ResolveAPIKey never selects or returns a raw token: only a SHA-256 digest is
// persisted. Ownership, expiry, and scopes are rechecked in write transactions.
func (s *Store) ResolveAPIKey(ctx context.Context, hash string, now int64) (Actor, error) {
	var actor Actor
	var scopes string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.password_hash, u.created_at, k.scopes, k.purpose
		FROM api_keys k JOIN users u ON u.id = k.user_id
		WHERE k.token_hash = ? AND (k.expires_at IS NULL OR k.expires_at > ?)
	`, hash, now).Scan(&actor.ID, &actor.Username, &actor.PasswordHash, &actor.CreatedAt, &scopes, &actor.APIKeyPurpose)
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
// database configuration or SQLite file. Browser management excludes these keys.
func (s *Store) RevokeLocalAPIKey(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM api_keys WHERE id = ? AND purpose = 'development'", id)
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

// Personal key management requires a fresh browser session, never a bearer key.
func (s *Store) beginKeyManagement(ctx context.Context, actor Actor) (*transaction, error) {
	if actor.APIKeyHash != "" || actor.SessionHash == "" {
		return nil, ErrForbidden
	}
	return s.beginAuthorized(ctx, actor, "")
}

func (s *Store) UserAPIKeys(ctx context.Context, actor Actor) ([]APIKey, error) {
	tx, err := s.beginKeyManagement(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, name, scopes, created_at, COALESCE(expires_at, 0) FROM api_keys
		WHERE user_id = ? AND purpose = 'production' ORDER BY created_at DESC, id`, actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []APIKey{}
	for rows.Next() {
		key := APIKey{UserID: actor.ID, Username: actor.Username}
		var scopes string
		if err := rows.Scan(&key.ID, &key.Name, &scopes, &key.CreatedAt, &key.ExpiresAt); err != nil {
			return nil, err
		}
		key.Scopes = strings.Split(scopes, ",")
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) CreateUserAPIKey(ctx context.Context, actor Actor, key APIKey, hash string) error {
	if key.UserID != actor.ID || key.Purpose != "production" {
		return ErrForbidden
	}
	tx, err := s.beginKeyManagement(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	expiry := sql.NullInt64{Int64: key.ExpiresAt, Valid: key.ExpiresAt != 0}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_keys(id, user_id, name, token_hash, scopes, created_at, expires_at, purpose)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'production')`, key.ID, actor.ID, key.Name, hash, strings.Join(key.Scopes, ","), key.CreatedAt, expiry)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeUserAPIKey(ctx context.Context, actor Actor, id string) error {
	tx, err := s.beginKeyManagement(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM api_keys WHERE id = ? AND user_id = ? AND purpose = 'production'", id, actor.ID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}
