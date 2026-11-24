package store

import (
	"context"
	"errors"
)

var ErrJobSecret = errors.New("provide a value for each new job secret")

// JobSecret contains ciphertext for internal persistence and worker snapshots.
// A nil ciphertext in an update preserves the existing value with that name.
type JobSecret struct {
	Name       string `json:"-"`
	Ciphertext []byte `json:"-"`
}

func jobSecretNames(ctx context.Context, tx *transaction, jobID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM job_secrets WHERE job_id = ? ORDER BY name`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func jobSecretValues(ctx context.Context, tx *transaction, jobID string) ([]JobSecret, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, encrypted_value FROM job_secrets WHERE job_id = ? ORDER BY name`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []JobSecret{}
	for rows.Next() {
		var secret JobSecret
		if err := rows.Scan(&secret.Name, &secret.Ciphertext); err != nil {
			return nil, err
		}
		values = append(values, secret)
	}
	return values, rows.Err()
}

// Copies resolve preserved values from source ciphertext already rebound to the
// new job. Edits resolve them from the current job. Merge the list once, inside
// the same transaction as authorization, version checks, and the definition.
func saveJobSecrets(ctx context.Context, tx *transaction, jobID string, changes, previous []JobSecret) error {
	if changes == nil {
		changes = previous
		if changes == nil {
			return nil
		}
	}
	if previous == nil {
		var err error
		previous, err = jobSecretValues(ctx, tx, jobID)
		if err != nil {
			return err
		}
	}
	existing := make(map[string][]byte, len(previous))
	for _, secret := range previous {
		existing[secret.Name] = secret.Ciphertext
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_secrets WHERE job_id = ?`, jobID); err != nil {
		return err
	}
	for _, secret := range changes {
		if secret.Ciphertext == nil {
			value, found := existing[secret.Name]
			if !found {
				return ErrJobSecret
			}
			secret.Ciphertext = value
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_secrets(job_id,name,encrypted_value) VALUES(?,?,?)`, jobID, secret.Name, secret.Ciphertext); err != nil {
			return err
		}
	}
	return nil
}
