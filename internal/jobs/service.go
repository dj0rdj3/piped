// Package jobs validates and persists shared shell-script job definitions.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"piped/internal/cron"
	"piped/internal/secrets"
	"piped/internal/store"
)

const MaxScriptBytes = 64 * 1024

var ErrInput = errors.New("invalid job")

type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }
func (e *InputError) Unwrap() error { return ErrInput }
func invalid(message string) error  { return &InputError{Message: message} }

type Service struct {
	store *store.Store
	vault *secrets.Vault
}

func New(ctx context.Context, db *store.Store, vault *secrets.Vault) (*Service, error) {
	if vault == nil {
		return nil, errors.New("job secret encryption is required")
	}
	if err := db.VerifyVault(ctx, vault); err != nil {
		return nil, err
	}
	return &Service{store: db, vault: vault}, nil
}

type Input struct {
	NotificationTargetIDs []string           `json:"notification_target_ids,omitempty"`
	CopyFrom              *store.JobCopy     `json:"copy_from,omitempty"`
	Schedule              *store.JobSchedule `json:"schedule,omitempty"`
	Secrets               []SecretInput      `json:"secrets,omitempty"`
	Name                  string             `json:"name"`
	Script                string             `json:"script"`
	HostIDs               []string           `json:"host_ids"`
	HostTags              []string           `json:"host_tags"`
	ExcludedHostIDs       []string           `json:"excluded_host_ids"`
	ExecutionMode         string             `json:"execution_mode"`
	RunAsSudo             bool               `json:"run_as_sudo"`
	StopOnFailure         bool               `json:"stop_on_failure"`
	Version               int64              `json:"version"`
}

func validID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16 && id == strings.ToLower(id)
}

func validate(input Input) (store.Job, error) {
	job := store.Job{
		NotificationTargetIDs: input.NotificationTargetIDs,
		Name:                  strings.TrimSpace(input.Name),
		Script:                input.Script,
		ExecutionMode:         input.ExecutionMode,
		RunAsSudo:             input.RunAsSudo,
		StopOnFailure:         input.StopOnFailure,
		Version:               input.Version,
	}
	nameLength := utf8.RuneCountInString(job.Name)
	if !utf8.ValidString(job.Name) || nameLength < 1 || nameLength > 100 || strings.ContainsFunc(job.Name, unicode.IsControl) {
		return job, invalid("name must contain 1-100 characters without control characters")
	}
	if !utf8.ValidString(job.Script) || strings.TrimSpace(job.Script) == "" || len(job.Script) > MaxScriptBytes || strings.ContainsRune(job.Script, 0) {
		return job, invalid("provide a shell script of at most 64 KiB without null characters")
	}
	if job.ExecutionMode != "sequential" && job.ExecutionMode != "parallel" {
		return job, invalid("execution mode must be sequential or parallel")
	}
	if len(input.HostIDs)+len(input.HostTags) < 1 || len(input.HostIDs)+len(input.HostTags) > store.MaxJobHosts {
		return job, invalid("select between 1 and 1000 individual hosts or tags")
	}
	job.HostIDs = append([]string{}, input.HostIDs...)
	job.HostTags = []string{}
	for _, tag := range input.HostTags {
		tag = strings.TrimSpace(tag)
		if !utf8.ValidString(tag) || utf8.RuneCountInString(tag) < 1 || utf8.RuneCountInString(tag) > 50 || strings.Contains(tag, ",") || strings.ContainsFunc(tag, unicode.IsControl) {
			return job, invalid("host tags must contain 1-50 characters without commas or control characters")
		}
		job.HostTags = append(job.HostTags, tag)
	}
	slices.Sort(job.HostTags)
	job.HostTags = slices.Compact(job.HostTags)
	seen := make(map[string]bool, len(job.HostIDs))
	for _, id := range job.HostIDs {
		if !validID(id) || seen[id] {
			return job, invalid("select valid hosts without duplicates")
		}
		seen[id] = true
	}
	if len(input.ExcludedHostIDs) > store.MaxJobHosts {
		return job, invalid("exclude at most 1000 hosts")
	}
	job.ExcludedHostIDs = append([]string{}, input.ExcludedHostIDs...)
	for _, id := range job.ExcludedHostIDs {
		if !validID(id) || seen[id] {
			return job, invalid("exclude valid hosts without duplicates or individually selected hosts")
		}
		seen[id] = true
	}
	slices.Sort(job.ExcludedHostIDs)
	if len(job.NotificationTargetIDs) > store.MaxJobNotificationTargets {
		return job, invalid("select at most 64 notification targets")
	}
	seenTargets := map[string]bool{}
	for _, id := range job.NotificationTargetIDs {
		if !validID(id) || seenTargets[id] {
			return job, invalid("select valid notification targets without duplicates")
		}
		seenTargets[id] = true
	}
	if input.Schedule != nil {
		schedule := *input.Schedule
		schedule.Cron = strings.Join(strings.Fields(schedule.Cron), " ")
		if len(input.Schedule.Cron) > 256 || strings.ContainsAny(input.Schedule.Cron, "\r\n\x00") {
			return job, invalid(cron.ErrSyntax.Error())
		}
		if schedule.Cron != "" {
			if _, err := cron.Parse(input.Schedule.Cron); err != nil {
				return job, invalid(err.Error())
			}
		} else if schedule.Enabled {
			return job, invalid("provide a cron expression before enabling the schedule")
		}
		job.Schedule = &schedule
	}
	return job, nil
}

func (s *Service) Save(ctx context.Context, actor store.Actor, id string, input Input) (store.Job, error) {
	if err := validateSecrets(input.Secrets); err != nil {
		return store.Job{}, err
	}
	job, err := validate(input)
	if err != nil {
		return store.Job{}, err
	}
	create := id == ""
	if input.CopyFrom != nil && (!create || !validID(input.CopyFrom.ID) || input.CopyFrom.Version < 1) {
		return store.Job{}, invalid("copy_from requires a valid source job ID and current version when creating a job")
	}
	if create {
		if input.Version != 0 {
			return store.Job{}, invalid("new jobs must not specify a version")
		}
		bytes := make([]byte, 16)
		_, _ = rand.Read(bytes)
		id = hex.EncodeToString(bytes)
	} else if !validID(id) || input.Version < 1 {
		return store.Job{}, invalid("provide a valid job ID and current version")
	}
	job.ID = id
	if input.CopyFrom != nil {
		return s.store.CopyJob(ctx, actor, job, s.encryptSecrets(id, input.Secrets), *input.CopyFrom, func(values []store.JobSecret) ([]store.JobSecret, error) {
			for index := range values {
				secret := &values[index]
				plain, err := s.vault.Open("job:"+input.CopyFrom.ID+":env:"+secret.Name, secret.Ciphertext)
				if err != nil {
					return nil, err
				}
				secret.Ciphertext = s.vault.Seal("job:"+id+":env:"+secret.Name, plain)
				clear(plain)
			}
			return values, nil
		})
	}
	return s.store.SaveJob(ctx, actor, job, s.encryptSecrets(id, input.Secrets), create)
}

func (s *Service) List(ctx context.Context, actor store.Actor) ([]store.Job, error) {
	return s.store.Jobs(ctx, actor)
}

func (s *Service) Delete(ctx context.Context, actor store.Actor, id string, version int64) error {
	if !validID(id) || version < 1 {
		return invalid("provide a valid job ID and current version")
	}
	return s.store.DeleteJob(ctx, actor, id, version)
}
