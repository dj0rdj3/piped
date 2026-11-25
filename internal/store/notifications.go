package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

const (
	MaxJobNotificationTargets = 64
	ScopeNotificationsRead    = "notifications:read"
	ScopeNotificationsWrite   = "notifications:write"
)

var (
	ErrNotificationSecret   = errors.New("provide a value for each new notification secret")
	ErrNotificationType     = errors.New("notification target type cannot be changed")
	ErrNotificationChanged  = errors.New("notification target changed; refresh and try again")
	ErrNotificationInUse    = errors.New("notification target is assigned to a job; remove it from the job first")
	ErrJobNotificationLimit = errors.New("select at most 64 notification targets")
	ErrJobNotification      = errors.New("a notification target no longer exists; refresh and try again")
)

type NotificationEndpoint struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type NotificationSecret struct {
	Name       string `json:"-"`
	Ciphertext []byte `json:"-"`
}

type NotificationTarget struct {
	Endpoint       *NotificationEndpoint `json:"endpoint"`
	SecretNames    []string              `json:"secret_names"`
	Secrets        []NotificationSecret  `json:"-"`
	Type           string                `json:"type"`
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	Method         string                `json:"method"`
	Body           string                `json:"body"`
	ContentType    string                `json:"content_type"`
	Enabled        bool                  `json:"enabled"`
	DefaultEnabled bool                  `json:"default_enabled"`
	Version        int64                 `json:"version"`
}

const notificationColumns = `id, name, method, body, content_type, enabled, default_enabled, version, target_type, endpoint`

func scanNotification(row interface{ Scan(...any) error }) (NotificationTarget, error) {
	target := NotificationTarget{SecretNames: []string{}}
	var endpoint string
	err := row.Scan(&target.ID, &target.Name, &target.Method, &target.Body, &target.ContentType, &target.Enabled, &target.DefaultEnabled, &target.Version, &target.Type, &endpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return target, ErrNotFound
	}
	if err == nil {
		target.Endpoint = &NotificationEndpoint{}
		if err := json.Unmarshal([]byte(endpoint), target.Endpoint); err != nil {
			return target, errors.New("invalid stored notification endpoint")
		}
	}
	return target, err
}

func (s *Store) NotificationTargets(ctx context.Context, actor Actor) ([]NotificationTarget, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeNotificationsRead)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+notificationColumns+` FROM notification_targets ORDER BY lower(name), name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []NotificationTarget{}
	indices := map[string]int{}
	for rows.Next() {
		target, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		indices[target.ID] = len(targets)
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT target_id, name FROM notification_secrets ORDER BY target_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		index := indices[id]
		targets[index].SecretNames = append(targets[index].SecretNames, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return targets, tx.Commit()
}

// SaveNotificationTarget resolves preserved secrets and validates the complete
// definition under the same authorization/version lock as its write.
func (s *Store) SaveNotificationTarget(ctx context.Context, actor Actor, target NotificationTarget, create bool, validate func(NotificationTarget) error) (NotificationTarget, error) {
	tx, err := s.beginAuthorized(ctx, actor, ScopeNotificationsWrite)
	if err != nil {
		return target, err
	}
	defer tx.Rollback()
	if create {
		target.Version = 1
	} else {
		current, err := scanNotification(tx.QueryRowContext(ctx, `SELECT `+notificationColumns+` FROM notification_targets WHERE id = ?`, target.ID))
		if err != nil {
			return target, err
		}
		if current.Version != target.Version {
			return target, ErrNotificationChanged
		}
		if target.Type != current.Type {
			return target, ErrNotificationType
		}
		if target.Endpoint == nil {
			target.Endpoint = current.Endpoint
		}
		target.Version++
	}
	previous, err := notificationSecretValues(ctx, tx, target.ID)
	if err != nil {
		return target, err
	}
	target.Secrets, err = resolveNotificationSecrets(previous, target.Secrets)
	if err != nil {
		return target, err
	}
	if validate != nil {
		if err := validate(target); err != nil {
			return target, err
		}
	}
	encoded, err := json.Marshal(target.Endpoint)
	if err != nil {
		return target, err
	}
	endpoint := string(encoded)
	if create {
		_, err = tx.ExecContext(ctx, `INSERT INTO notification_targets (`+notificationColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, target.ID, target.Name, target.Method, target.Body, target.ContentType, target.Enabled, target.DefaultEnabled, target.Version, target.Type, endpoint)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE notification_targets SET name = ?, method = ?, body = ?, content_type = ?, enabled = ?, default_enabled = ?, version = ?, endpoint = ? WHERE id = ?`, target.Name, target.Method, target.Body, target.ContentType, target.Enabled, target.DefaultEnabled, target.Version, endpoint, target.ID)
	}
	if err != nil {
		return target, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notification_secrets WHERE target_id = ?`, target.ID); err != nil {
		return target, err
	}
	target.SecretNames = []string{}
	for _, secret := range target.Secrets {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_secrets(target_id, name, encrypted_value) VALUES (?, ?, ?)`, target.ID, secret.Name, secret.Ciphertext); err != nil {
			return target, err
		}
		target.SecretNames = append(target.SecretNames, secret.Name)
	}
	slices.Sort(target.SecretNames)
	target.Secrets = nil
	return target, tx.Commit()
}

// Resolve changes without rebinding ciphertext to a differently cased name.
func resolveNotificationSecrets(previous, changes []NotificationSecret) ([]NotificationSecret, error) {
	if changes == nil {
		return previous, nil
	}
	existing := map[string]NotificationSecret{}
	ambiguous := map[string]bool{}
	for _, secret := range previous {
		name := strings.ToLower(secret.Name)
		if _, exists := existing[name]; exists {
			ambiguous[name] = true
		}
		existing[name] = secret
	}
	for index := range changes {
		if changes[index].Ciphertext == nil {
			name := strings.ToLower(changes[index].Name)
			value, exists := existing[name]
			if !exists || ambiguous[name] {
				return nil, ErrNotificationSecret
			}
			// Keep the stored spelling with its authenticated ciphertext. A
			// case-only edit must not change the vault's associated data.
			changes[index] = value
		}
	}

	return changes, nil
}

func notificationSecretValues(ctx context.Context, tx *transaction, id string) ([]NotificationSecret, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, encrypted_value FROM notification_secrets WHERE target_id = ? ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []NotificationSecret{}
	for rows.Next() {
		var secret NotificationSecret
		if err := rows.Scan(&secret.Name, &secret.Ciphertext); err != nil {
			return nil, err
		}
		values = append(values, secret)
	}
	return values, rows.Err()
}

func (s *Store) DeleteNotificationTarget(ctx context.Context, actor Actor, id string, version int64) error {
	tx, err := s.beginAuthorized(ctx, actor, ScopeNotificationsWrite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM notification_targets WHERE id = ?`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != version {
		return ErrNotificationChanged
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notification_targets WHERE id = ?`, id); err != nil {
		if foreignKeyViolation(err) {
			return ErrNotificationInUse
		}
		return err
	}
	return tx.Commit()
}

func jobNotificationIDs(ctx context.Context, tx *transaction, jobID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT target_id FROM job_notification_targets WHERE job_id = ? ORDER BY target_id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func saveJobNotifications(ctx context.Context, tx *transaction, job *Job, create bool, source *JobCopy) error {
	if job.NotificationTargetIDs == nil {
		var err error
		switch {
		case source != nil:
			job.NotificationTargetIDs, err = jobNotificationIDs(ctx, tx, source.ID)
		case !create:
			job.NotificationTargetIDs, err = jobNotificationIDs(ctx, tx, job.ID)
		default:
			rows, queryErr := tx.QueryContext(ctx, `SELECT id FROM notification_targets WHERE default_enabled = 1 AND enabled = 1 ORDER BY id`)
			if queryErr != nil {
				return queryErr
			}
			job.NotificationTargetIDs = []string{}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					break
				}
				job.NotificationTargetIDs = append(job.NotificationTargetIDs, id)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		if err != nil {
			return err
		}
	}
	if len(job.NotificationTargetIDs) > MaxJobNotificationTargets {
		return ErrJobNotificationLimit
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_notification_targets WHERE job_id = ?`, job.ID); err != nil {
		return err
	}
	for _, id := range job.NotificationTargetIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_notification_targets (job_id, target_id) VALUES (?, ?)`, job.ID, id); err != nil {
			if foreignKeyViolation(err) {
				return ErrJobNotification
			}
			return err
		}
	}
	slices.Sort(job.NotificationTargetIDs)
	return nil
}

// notificationSnapshot runs inside admission, so edits cannot redirect an active run.
func notificationSnapshot(ctx context.Context, tx *transaction, jobID string) ([]NotificationTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT n.id, n.name, n.method, n.body, n.content_type, n.enabled, n.default_enabled, n.version, n.target_type, n.endpoint FROM notification_targets n JOIN job_notification_targets j ON j.target_id = n.id WHERE j.job_id = ? AND n.enabled = 1 ORDER BY n.id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []NotificationTarget{}
	for rows.Next() {
		target, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range targets {
		targets[index].Secrets, err = notificationSecretValues(ctx, tx, targets[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return targets, nil
}

// NotificationOutput is internal worker access to already masked, persisted output.
// Bound memory even for very large logs and a single unterminated line.
func (s *Store) NotificationOutput(ctx context.Context, runID, hostID string) (string, error) {
	reader := outputReader{store: s, ctx: ctx, runID: runID, hostID: hostID, descending: true}
	chunks, _, err := outputPage(&reader, 0, 0)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	for _, chunk := range chunks {
		output.Write(chunk.Data)
	}
	return output.String(), nil
}
