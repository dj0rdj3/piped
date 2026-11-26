package store

import "context"

// Both admission and search use this membership predicate. Explicit ordering is
// an admission concern; exclusions apply equally to both sources of membership.
const effectiveHostMembership = `
 (EXISTS(SELECT 1 FROM job_hosts j WHERE j.job_id = ? AND j.host_id = h.id)
 OR EXISTS(SELECT 1 FROM job_host_tags j JOIN host_tags t ON t.tag = j.tag
  WHERE j.job_id = ? AND t.host_id = h.id))
 AND NOT EXISTS(SELECT 1 FROM job_host_exclusions x WHERE x.job_id = ? AND x.host_id = h.id)`

func jobHostIDs(ctx context.Context, tx writeTransaction, jobID string) ([]string, error) {
	ids, err := tx.queryStrings(ctx, `SELECT h.id FROM hosts h
 LEFT JOIN job_hosts selected ON selected.job_id = ? AND selected.host_id = h.id
 WHERE `+effectiveHostMembership+`
 ORDER BY CASE WHEN selected.host_id IS NULL THEN 1 ELSE 0 END,
 selected.position, lower(h.name), h.name, h.id LIMIT ?`, jobID, jobID, jobID, jobID, MaxJobHosts+1)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 || len(ids) > MaxJobHosts {
		return nil, ErrJobTargets
	}
	return ids, nil
}
