package store

import (
	"context"
	"encoding/json"
)

// Resolve inside the run's admission transaction. Explicit hosts retain their
// saved order; tag matches follow in name/ID order, with every host included once.
func jobHostIDs(ctx context.Context, tx *transaction, jobID string) ([]string, error) {
	ids, err := tx.queryStrings(ctx, `SELECT host_id FROM job_hosts j WHERE job_id = ?
		AND NOT EXISTS (SELECT 1 FROM job_host_exclusions x WHERE x.job_id = j.job_id AND x.host_id = j.host_id)
		ORDER BY position`, jobID)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	values, err := tx.queryStrings(ctx, `SELECT tag FROM job_host_tags WHERE job_id = ?`, jobID)
	if err != nil {
		return nil, err
	}
	tags := make(map[string]bool, len(values))
	for _, tag := range values {
		tags[tag] = true
	}
	if len(tags) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT id, tags FROM hosts h
			WHERE NOT EXISTS (SELECT 1 FROM job_host_exclusions x WHERE x.job_id = ? AND x.host_id = h.id)
			ORDER BY lower(name), name, id`, jobID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id, encoded string
			if err := rows.Scan(&id, &encoded); err != nil {
				return nil, err
			}
			if selected[id] {
				continue
			}
			var hostTags []string
			if err := json.Unmarshal([]byte(encoded), &hostTags); err != nil {
				return nil, err
			}
			for _, tag := range hostTags {
				if tags[tag] {
					ids = append(ids, id)
					selected[id] = true
					break
				}
			}
			if len(ids) > MaxJobHosts {
				return nil, ErrJobTargets
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(ids) == 0 || len(ids) > MaxJobHosts {
		return nil, ErrJobTargets
	}
	return ids, nil
}
