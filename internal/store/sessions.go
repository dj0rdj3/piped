package store

import "context"

func (s *Store) CreateSession(ctx context.Context, user User, hash string, now, expires int64) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, created_at, expires_at)
		SELECT ?, id, ?, ? FROM users WHERE id = ? AND password_hash = ?`, hash, now, expires, user.ID, user.PasswordHash)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUnauthorized
	}
	return nil
}

func (s *Store) Session(ctx context.Context, hash string, now int64) (Actor, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.password_hash, u.created_at
		FROM users u JOIN sessions s ON s.user_id = u.id WHERE s.token_hash = ? AND s.expires_at > ?`, hash, now))
	return Actor{User: u, SessionHash: hash}, err
}

func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", hash)
	return err
}

func (s *Store) PurgeSessions(ctx context.Context, now int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", now)
	return err
}
