package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// beginAuthorized checks fresh session/key authority under the same database
// lock as the operation. Callers still own rollback and commit on success.
func (s *Store) beginAuthorized(ctx context.Context, actor Actor, scope string) (*transaction, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := authorize(ctx, tx, actor, scope); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// Actor binds privileged changes to the session and password checked by the caller.
type Actor struct {
	User
	SessionHash string   `json:"-"`
	APIKeyHash  string   `json:"-"`
	Scopes      []string `json:"-"`
}

func authorize(ctx context.Context, tx *transaction, actor Actor, scope string) error {
	if actor.APIKeyHash != "" {
		var scopes string
		err := tx.QueryRowContext(ctx, `
			SELECT k.scopes FROM api_keys k JOIN users u ON u.id = k.user_id
			WHERE k.token_hash = ? AND u.id = ? AND u.password_hash = ? AND k.expires_at > ?
		`, actor.APIKeyHash, actor.ID, actor.PasswordHash, time.Now().Unix()).Scan(&scopes)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthorized
		}
		if err != nil {
			return err
		}
		if !HasScope(strings.Split(scopes, ","), scope) {
			return ErrForbidden
		}
		return nil
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u JOIN sessions s ON s.user_id = u.id
		WHERE u.id = ? AND u.password_hash = ? AND s.token_hash = ? AND s.expires_at > ?`,
		actor.ID, actor.PasswordHash, actor.SessionHash, time.Now().Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUnauthorized
	}
	return nil
}

func (s *Store) Authorize(ctx context.Context, actor Actor, scope string) error {
	tx, err := s.beginAuthorized(ctx, actor, scope)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return nil
}
