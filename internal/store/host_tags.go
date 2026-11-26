package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
)

// Host membership has one authoritative representation. The legacy JSON column
// remains solely to preserve immutable migrations and upgrade existing data.
func (s *Store) initializeHostTags(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ready int
	err = tx.QueryRowContext(ctx, `SELECT version FROM list_state WHERE version = 2`).Scan(&ready)
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, tags FROM hosts`)
	if err != nil {
		return err
	}
	hosts := []Host{}
	for rows.Next() {
		var host Host
		var encoded string
		if err := rows.Scan(&host.ID, &encoded); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(encoded), &host.Tags); err != nil {
			rows.Close()
			return err
		}
		hosts = append(hosts, host)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, host := range hosts {
		if err := saveHostTags(ctx, tx, host.ID, host.Tags); err != nil {
			return err
		}
	}
	// Retire the derived membership cache; list queries use host_tags directly.
	if _, err := tx.ExecContext(ctx, `DELETE FROM list_tags`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO list_state(version) VALUES (2)`); err != nil {
		return err
	}
	return tx.Commit()
}

func saveHostTags(ctx context.Context, tx writeTransaction, id string, tags []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM host_tags WHERE host_id = ?`, id); err != nil {
		return err
	}
	values := slices.Clone(tags)
	slices.Sort(values)
	for _, tag := range slices.Compact(values) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO host_tags(host_id, tag) VALUES (?, ?)`, id, tag); err != nil {
			return err
		}
	}
	return nil
}

func readHostTags(ctx context.Context, tx writeTransaction, hosts []Host) error {
	if len(hosts) == 0 {
		return nil
	}
	ids := make([]string, 0, len(hosts))
	positions := make(map[string]int, len(hosts))
	for index := range hosts {
		ids = append(ids, hosts[index].ID)
		positions[hosts[index].ID] = index
		hosts[index].Tags = []string{}
	}
	rows, err := tx.QueryContext(ctx, `SELECT host_id, tag FROM host_tags`+listIDFilter("host_id", ids), listIDArgs(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return err
		}
		index, ok := positions[id]
		if !ok {
			return errors.New("unexpected host tag owner")
		}
		hosts[index].Tags = append(hosts[index].Tags, tag)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range hosts {
		slices.Sort(hosts[index].Tags)
	}
	return rows.Close()
}
