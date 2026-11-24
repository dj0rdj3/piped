package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrHostName        = errors.New("host name already exists")
	ErrHostChanged     = errors.New("host changed; refresh and try again")
	ErrHostCredential  = errors.New("SSH key credential not found")
	ErrHostPassword    = errors.New("SSH password is required")
	ErrCredentialInUse = errors.New("credential is assigned to a host")
)

// Host is public metadata only. Passwords are never selected by list queries.
type Host struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Address         string   `json:"address"`
	Port            int      `json:"port"`
	Username        string   `json:"username"`
	CredentialID    string   `json:"credential_id"`
	CredentialName  string   `json:"credential_name"`
	HasPassword     bool     `json:"has_password"`
	RunAsSudo       bool     `json:"run_as_sudo"`
	HasSudoPassword bool     `json:"has_sudo_password"`
	Tags            []string `json:"tags"`
	Version         int64    `json:"version"`
	CreatedAt       int64    `json:"created_at"`
}

const hostColumns = `h.id, h.name, h.address, h.port, h.username,
    COALESCE(h.credential_id, ''), COALESCE(c.name, ''), h.encrypted_password IS NOT NULL,
    h.run_as_sudo, h.encrypted_sudo_password IS NOT NULL, h.tags, h.version, h.created_at`

func scanHost(row interface{ Scan(...any) error }) (Host, error) {
	var host Host
	var tags string
	err := row.Scan(&host.ID, &host.Name, &host.Address, &host.Port, &host.Username,
		&host.CredentialID, &host.CredentialName, &host.HasPassword,
		&host.RunAsSudo, &host.HasSudoPassword, &tags, &host.Version, &host.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, ErrNotFound
	}
	if err != nil {
		return Host{}, err
	}
	if err := json.Unmarshal([]byte(tags), &host.Tags); err != nil {
		return Host{}, err
	}
	return host, nil
}

func (s *Store) Hosts(ctx context.Context, actor Actor) ([]Host, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeHostsRead)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+hostColumns+`
        FROM hosts h LEFT JOIN credentials c ON c.id = h.credential_id
        ORDER BY lower(h.name), h.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Host{}
	for rows.Next() {
		host, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, host)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return items, tx.Commit()
}

// SaveHost checks authorization, references, version, and secret preservation in
// one transaction. Nil secrets preserve existing values; an empty sudo secret clears it.
func (s *Store) SaveHost(ctx context.Context, actor Actor, host Host, password, sudoPassword []byte, create bool) (Host, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeHostsWrite)
	if err != nil {
		return Host{}, err
	}
	defer tx.Rollback()
	if !create {
		var previousVersion int64
		var previousPassword, previousSudo []byte
		err := tx.QueryRowContext(ctx, `SELECT version, encrypted_password, encrypted_sudo_password FROM hosts WHERE id = ?`, host.ID).
			Scan(&previousVersion, &previousPassword, &previousSudo)
		if errors.Is(err, sql.ErrNoRows) {
			return Host{}, ErrNotFound
		}
		if err != nil {
			return Host{}, err
		}
		if host.Version != previousVersion {
			return Host{}, ErrHostChanged
		}
		if password == nil {
			password = previousPassword
		}
		if sudoPassword == nil {
			sudoPassword = previousSudo
		}
	}
	var credentialID any
	if host.CredentialID != "" {
		credentialID = host.CredentialID
		password = nil
	} else if len(password) == 0 {
		return Host{}, ErrHostPassword
	}
	if !host.RunAsSudo || len(sudoPassword) == 0 {
		sudoPassword = nil
	}
	tags, err := json.Marshal(host.Tags)
	if err != nil {
		return Host{}, err
	}
	if create {
		host.CreatedAt = time.Now().Unix()
		host.Version = 1
		_, err = tx.ExecContext(ctx, `INSERT INTO hosts
            (id, name, address, port, username, credential_id, encrypted_password,
            run_as_sudo, encrypted_sudo_password, tags, version, created_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			host.ID, host.Name, host.Address, host.Port, host.Username, credentialID, password,
			host.RunAsSudo, sudoPassword, string(tags), host.Version, host.CreatedAt)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE hosts SET name = ?, address = ?, port = ?,
            username = ?, credential_id = ?, encrypted_password = ?, run_as_sudo = ?,
            encrypted_sudo_password = ?, tags = ?, version = version + 1 WHERE id = ?`,
			host.Name, host.Address, host.Port, host.Username, credentialID, password,
			host.RunAsSudo, sudoPassword, string(tags), host.ID)
	}
	if err != nil {
		if foreignKeyViolation(err) {
			return Host{}, ErrHostCredential
		}
		if uniqueViolation(err) {
			return Host{}, ErrHostName
		}
		return Host{}, err
	}
	saved, err := scanHost(tx.QueryRowContext(ctx, `SELECT `+hostColumns+`
        FROM hosts h LEFT JOIN credentials c ON c.id = h.credential_id WHERE h.id = ?`, host.ID))
	if err != nil {
		return Host{}, err
	}
	return saved, tx.Commit()
}

func (s *Store) DeleteHost(ctx context.Context, actor Actor, id string, version int64) error {
	tx, err := s.beginAuthorized(ctx, actor, ScopeHostsWrite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRowContext(ctx, "SELECT version FROM hosts WHERE id = ?", id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != version {
		return ErrHostChanged
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM hosts WHERE id = ?", id); err != nil {
		if foreignKeyViolation(err) {
			return ErrHostInUse
		}
		return err
	}
	return tx.Commit()
}
