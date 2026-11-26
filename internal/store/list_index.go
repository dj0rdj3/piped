package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// List metadata contains only fields already exposed by resource lists. Go case
// folding and binary keys keep Unicode search and cursors identical on each DB.
// Source writes update this index in their own authorized transaction.
func refreshListEntry(ctx context.Context, tx writeTransaction, kind, id string) error {
	var name, search, hostSearch string
	var owner int64
	var tags []string
	var err error
	switch kind {
	case "users":
		err = tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id = ?`, id).Scan(&name)
		search = name
	case "credentials":
		var fingerprint, algorithm string
		err = tx.QueryRowContext(ctx, `SELECT name, fingerprint, algorithm FROM credentials WHERE id = ?`, id).Scan(&name, &fingerprint, &algorithm)
		search = strings.Join([]string{name, fingerprint, algorithm}, " ")
	case "hosts":
		var address, username, credential string
		err = tx.QueryRowContext(ctx, `SELECT h.name, h.address, h.username, COALESCE(c.name, '')
			FROM hosts h LEFT JOIN credentials c ON c.id = h.credential_id WHERE h.id = ?`, id).
			Scan(&name, &address, &username, &credential)
		if err == nil {
			tags, err = tx.queryStrings(ctx, `SELECT tag FROM host_tags WHERE host_id = ? ORDER BY tag`, id)
		}
		search = strings.Join(append([]string{name, address, username, credential}, tags...), " ")
	case "jobs":
		err = tx.QueryRowContext(ctx, `SELECT name FROM jobs WHERE id = ?`, id).Scan(&name)
		if err == nil {
			tags, err = tx.queryStrings(ctx, `SELECT tag FROM job_host_tags WHERE job_id = ? ORDER BY tag`, id)
		}
		text := append([]string{name}, tags...)
		search = strings.Join(text, " ")
		if err == nil {
			var hosts []string
			hosts, err = tx.queryStrings(ctx, `SELECT h.name FROM hosts h WHERE `+effectiveHostMembership+` ORDER BY lower(h.name), h.name, h.id`, id, id, id)
			hostSearch = strings.Join(append(text, hosts...), " ")
		}
		// Only hosts expose a tag filter. Job tags are searchable text.
		tags = nil
	case "notifications":
		var targetType string
		err = tx.QueryRowContext(ctx, `SELECT name, target_type FROM notification_targets WHERE id = ?`, id).Scan(&name, &targetType)
		search = name + " " + targetType
	case "keys":
		var created int64
		err = tx.QueryRowContext(ctx, `SELECT name, user_id, created_at FROM api_keys WHERE id = ? AND purpose = 'production'`, id).Scan(&search, &owner, &created)
		name = fmt.Sprintf("%020d", int64(math.MaxInt64)-created)
	default:
		return errors.New("unknown list resource")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, deleteErr := tx.ExecContext(ctx, `DELETE FROM list_entries WHERE kind = ? AND id = ?`, kind, id); deleteErr != nil {
		return deleteErr
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	key := []byte(strings.ToLower(name) + "\x00" + name)
	if _, err := tx.ExecContext(ctx, `INSERT INTO list_entries(kind, id, owner_id, name_key, search_text, host_search) VALUES (?, ?, ?, ?, ?, ?)`,
		kind, id, owner, key, strings.ToLower(search), strings.ToLower(hostSearch)); err != nil {
		return err
	}
	return nil
}

// Find affected definitions before changing host tags, then refresh after the
// host index is written. Dynamic tag membership and exclusions remain current.
func affectedListJobs(ctx context.Context, tx writeTransaction, hostID string, tags []string) ([]string, error) {
	query := `SELECT id FROM jobs WHERE id IN (SELECT job_id FROM job_hosts WHERE host_id = ?)
		OR id IN (SELECT job_id FROM job_host_tags WHERE tag IN (SELECT tag FROM host_tags WHERE host_id = ?)`
	args := []any{hostID, hostID}
	if len(tags) > 0 {
		query += ` OR tag IN (` + placeholders(len(tags)) + `)`
		for _, tag := range tags {
			args = append(args, tag)
		}
	}
	return tx.queryStrings(ctx, query+`)`, args...)
}

func refreshListJobs(ctx context.Context, tx writeTransaction, ids []string) error {
	for _, id := range ids {
		if err := refreshListEntry(ctx, tx, "jobs", id); err != nil {
			return err
		}
	}
	return nil
}

// Backfill once, atomically, after additive schema migrations. A failed or
// interrupted conversion leaves source records untouched and is retried on open.
func (s *Store) initializeLists(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ready int
	err = tx.QueryRowContext(ctx, `SELECT version FROM list_state WHERE version = 1`).Scan(&ready)
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Resource metadata is rebuilt under one transaction.
	for _, source := range []struct{ kind, table string }{
		{"users", "users"}, {"credentials", "credentials"}, {"hosts", "hosts"},
		{"jobs", "jobs"}, {"notifications", "notification_targets"}, {"keys", "api_keys"},
	} {
		ids, err := tx.queryStrings(ctx, `SELECT id FROM `+source.table)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := refreshListEntry(ctx, tx, source.kind, id); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO list_state(version) VALUES (1)`); err != nil {
		return err
	}
	return tx.Commit()
}

func placeholders(count int) string { return strings.TrimSuffix(strings.Repeat("?,", count), ",") }

func userListID(id int64) string { return strconv.FormatInt(id, 10) }
