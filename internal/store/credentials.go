package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"piped/internal/secrets"
)

var ErrCredentialName = errors.New("credential name already exists")

// Credential contains only public metadata. Secret material is kept in a
// separate database column and is never selected by UI-facing queries.
type Credential struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	PublicKey     string `json:"public_key"`
	Fingerprint   string `json:"fingerprint"`
	Algorithm     string `json:"algorithm"`
	HasPassphrase bool   `json:"has_passphrase"`
	CreatedAt     int64  `json:"created_at"`
}

// VerifyVault atomically pins the database to its deployment key. A missing
// verification record in a database with secrets is corruption, not a new vault.
func (s *Store) VerifyVault(ctx context.Context, v *secrets.Vault) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var encrypted []byte
	err = tx.QueryRowContext(ctx, "SELECT verification FROM encryption_settings WHERE id = 1").Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		var populated bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM credentials)
			    OR EXISTS(SELECT 1 FROM hosts)
			    OR EXISTS(SELECT 1 FROM job_secrets)
			    OR EXISTS(SELECT 1 FROM notification_secrets)
		`).Scan(&populated); err != nil {
			return err
		}
		if populated {
			return secrets.ErrDecrypt
		}
		verification := v.Seal("vault-verification", []byte("piped encryption key verification"))
		_, err = tx.ExecContext(ctx,
			"INSERT INTO encryption_settings(id, verification) VALUES (1, ?)",
			verification,
		)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		plain, err := v.Open("vault-verification", encrypted)
		if err != nil {
			return err
		}
		defer clear(plain)
		if string(plain) != "piped encryption key verification" {
			return secrets.ErrDecrypt
		}
	}
	return tx.Commit()
}

func (s *Store) Credentials(ctx context.Context, actor Actor) ([]Credential, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeCredentialsRead)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, public_key, fingerprint, algorithm, has_passphrase, created_at
		FROM credentials
		ORDER BY lower(name), name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Credential{}
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.Name, &c.PublicKey, &c.Fingerprint, &c.Algorithm, &c.HasPassphrase, &c.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return items, tx.Commit()
}

func credentialError(err error) error {
	if foreignKeyViolation(err) {
		return ErrCredentialInUse
	}
	if uniqueViolation(err) {
		return ErrCredentialName
	}
	return err
}

func (s *Store) CreateCredential(ctx context.Context, actor Actor, c Credential, encrypted []byte) (Credential, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeCredentialsWrite)
	if err != nil {
		return Credential{}, err
	}
	defer tx.Rollback()
	c.CreatedAt = time.Now().Unix()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO credentials(
			id, name, public_key, fingerprint, algorithm,
			has_passphrase, encrypted_secret, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		c.ID, c.Name, c.PublicKey, c.Fingerprint, c.Algorithm,
		c.HasPassphrase, encrypted, c.CreatedAt,
	)
	if err != nil {
		return Credential{}, credentialError(err)
	}
	return c, tx.Commit()
}

func (s *Store) DeleteCredential(ctx context.Context, actor Actor, id string) error {
	tx, err := s.beginAuthorized(ctx, actor, ScopeCredentialsWrite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM credentials WHERE id = ?", id)
	if err != nil {
		return credentialError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) RenameCredential(ctx context.Context, actor Actor, id, name string) error {
	tx, err := s.beginAuthorized(ctx, actor, ScopeCredentialsWrite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE credentials SET name = ? WHERE id = ?", name, id)
	if err != nil {
		return credentialError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}
