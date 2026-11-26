package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	CreatedAt    int64  `json:"created_at"`
	PasswordHash string `json:"-"`
}

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) UserByName(ctx context.Context, username string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT id, username, password_hash, created_at FROM users WHERE lower(username) = lower(?)", username))
}

// Users uses the same transaction for fresh authority and public rows.
func (s *Store) Users(ctx context.Context, actor Actor) ([]User, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeUsersRead)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	users, err := readUsers(ctx, tx, actor, nil)
	if err != nil {
		return nil, err
	}
	return users, tx.Commit()
}

func insertUser(ctx context.Context, tx *transaction, username, hash string) (User, error) {
	now := time.Now().Unix()
	query := "INSERT INTO users(username, password_hash, created_at) VALUES (?, ?, ?)"
	var id int64
	var err error
	if tx.database.dialect == "postgres" {
		err = tx.QueryRowContext(ctx, query+" RETURNING id", username, hash, now).Scan(&id)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, query, username, hash, now)
		if err == nil {
			id, err = result.LastInsertId()
		}
	}
	if uniqueViolation(err) {
		return User{}, ErrConflict
	}
	if err != nil {
		return User{}, err
	}
	if err := refreshListEntry(ctx, tx, "users", userListID(id)); err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, PasswordHash: hash, CreatedAt: now}, err
}

func (s *Store) InitAdmin(ctx context.Context, username, hash string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return User{}, err
	}
	if count != 0 {
		return User{}, ErrInitialized
	}
	u, err := insertUser(ctx, tx, username, hash)
	if err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}

func (s *Store) CreateUser(ctx context.Context, actor Actor, username, hash string) (User, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeUsersWrite)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	u, err := insertUser(ctx, tx.transaction, username, hash)
	if err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}

func (s *Store) DeleteUser(ctx context.Context, actor Actor, id int64) error {
	tx, err := s.beginAuthorized(ctx, actor, ScopeUsersWrite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count <= 1 {
		return ErrLastUser
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
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
	if err := refreshListEntry(ctx, tx, "users", userListID(id)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM list_entries WHERE kind = 'keys' AND owner_id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ChangePassword(ctx context.Context, actor Actor, id int64, hash string) error {
	tx, err := s.beginAuthorized(ctx, actor, ScopeUsersWrite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", hash, id)
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
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM api_keys WHERE user_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM list_entries WHERE kind = 'keys' AND owner_id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}
