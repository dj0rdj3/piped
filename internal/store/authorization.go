package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// authorizedTransaction can only be constructed after fresh authority is checked
// under the writer lock. Privileged resource readers require this capability.
type authorizedTransaction struct{ *transaction }

// writeTransaction is used only by private helpers shared with fenced service
// operations. It conveys no user authority by itself.
type writeTransaction interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	queryStrings(context.Context, string, ...any) ([]string, error)
}

// beginAuthorized checks fresh session/key authority under the same database
// lock as the operation. Callers still own rollback and commit on success.
func (s *Store) beginAuthorized(ctx context.Context, actor Actor, scope string) (*authorizedTransaction, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := authorize(ctx, tx, actor, scope); err != nil {
		tx.Rollback()
		return nil, err
	}
	return &authorizedTransaction{transaction: tx}, nil
}

// Actor binds privileged changes to the session and password checked by the caller.
type Actor struct {
	User
	SessionHash   string   `json:"-"`
	APIKeyPurpose string   `json:"-"`
	APIKeyHash    string   `json:"-"`
	Scopes        []string `json:"-"`
}

func authorize(ctx context.Context, tx writeTransaction, actor Actor, scope string) error {
	if actor.APIKeyHash != "" {
		var scopes string
		err := tx.QueryRowContext(ctx, `
			SELECT k.scopes FROM api_keys k JOIN users u ON u.id = k.user_id
			WHERE k.token_hash = ? AND k.purpose = ? AND u.id = ? AND u.password_hash = ? AND (k.expires_at IS NULL OR k.expires_at > ?)
		`, actor.APIKeyHash, actor.APIKeyPurpose, actor.ID, actor.PasswordHash, time.Now().Unix()).Scan(&scopes)
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
