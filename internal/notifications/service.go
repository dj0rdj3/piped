package notifications

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"piped/internal/secrets"
	"piped/internal/store"
)

var ErrInput = errors.New("invalid notification target")

type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }
func (e *InputError) Unwrap() error { return ErrInput }
func invalid(message string) error  { return &InputError{message} }

type Endpoint = store.NotificationEndpoint

type Input struct {
	Secrets        []SecretInput `json:"secrets,omitempty"`
	Type           string        `json:"type"`
	Name           string        `json:"name"`
	Method         string        `json:"method"`
	Body           string        `json:"body"`
	ContentType    string        `json:"content_type"`
	Enabled        bool          `json:"enabled"`
	DefaultEnabled bool          `json:"default_enabled"`
	Version        int64         `json:"version"`
	Endpoint       *Endpoint     `json:"endpoint,omitempty"`
}

type Service struct {
	db     *store.Store
	vault  *secrets.Vault
	client *http.Client
	slots  chan struct{}
}

func New(ctx context.Context, db *store.Store, vault *secrets.Vault) (*Service, error) {
	if vault == nil {
		return nil, errors.New("notification encryption is required")
	}
	if err := db.VerifyVault(ctx, vault); err != nil {
		return nil, err
	}
	// No ambient proxies or redirects: endpoint credentials must stay at the
	// explicitly configured destination. Internal homelab URLs are supported.
	transport := &http.Transport{
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 16 * 1024,
		DisableKeepAlives:      true,
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Service{db: db, vault: vault, client: client, slots: make(chan struct{}, 4)}, nil
}

func validID(id string) bool {
	data, err := hex.DecodeString(id)
	return err == nil && len(data) == 16 && strings.ToLower(id) == id
}

func (s *Service) List(ctx context.Context, actor store.Actor) ([]store.NotificationTarget, error) {
	return s.db.NotificationTargets(ctx, actor)
}

func (s *Service) Save(ctx context.Context, actor store.Actor, id string, input Input) (store.NotificationTarget, error) {
	if err := validateSecretInputs(input.Secrets); err != nil {
		return store.NotificationTarget{}, err
	}
	target := store.NotificationTarget{
		Endpoint:       input.Endpoint,
		Type:           input.Type,
		ID:             id,
		Name:           strings.TrimSpace(input.Name),
		Method:         input.Method,
		Body:           input.Body,
		ContentType:    input.ContentType,
		Enabled:        input.Enabled,
		DefaultEnabled: input.DefaultEnabled,
		Version:        input.Version,
	}
	if target.Type != "webhook" {
		return target, invalid("unsupported notification type")
	}
	if !utf8.ValidString(target.Name) || utf8.RuneCountInString(target.Name) < 1 || utf8.RuneCountInString(target.Name) > 100 || strings.ContainsFunc(target.Name, unicode.IsControl) {
		return target, invalid("name must contain 1-100 characters without control characters")
	}
	if target.Method != "POST" && target.Method != "PUT" && target.Method != "GET" {
		return target, invalid("method must be POST, PUT, or GET")
	}
	if target.ContentType != "application/json" && target.ContentType != "text/plain" {
		return target, invalid("body format must be application/json or text/plain")
	}
	if len(target.Body) > 64*1024 || strings.ContainsRune(target.Body, 0) {
		return target, invalid("body must be at most 64 KiB without null characters")
	}
	if target.Method == "GET" && target.Body != "" {
		return target, invalid("GET webhooks must have an empty plain-text body")
	}
	create := id == ""
	if create {
		if input.Version != 0 || input.Endpoint == nil {
			return target, invalid("new targets require endpoint settings and no version")
		}
		random := make([]byte, 16)
		_, _ = rand.Read(random)
		target.ID = hex.EncodeToString(random)
	} else if !validID(id) || input.Version < 1 {
		return target, invalid("provide a valid target ID and current version")
	}
	target.Secrets = s.encryptSecretChanges(target.ID, input.Secrets)
	return s.db.SaveNotificationTarget(ctx, actor, target, create, func(target store.NotificationTarget) error {
		_, _, err := s.render(target, Event{})
		return err
	})
}

func (s *Service) Delete(ctx context.Context, actor store.Actor, id string, version int64) error {
	if !validID(id) || version < 1 {
		return invalid("provide a valid target ID and current version")
	}
	return s.db.DeleteNotificationTarget(ctx, actor, id, version)
}

// Use the same rendering path for transactional saves and admitted deliveries.
// Expanded values stay local and are never copied back into saved templates.
func (s *Service) render(target store.NotificationTarget, event Event) (Endpoint, string, error) {
	if target.Type != "webhook" {
		return Endpoint{}, "", invalid("unsupported notification type")
	}
	if target.Endpoint == nil {
		return Endpoint{}, "", invalid("provide webhook endpoint settings")
	}
	values, err := s.secretValues(target)
	if err != nil {
		return Endpoint{}, "", err
	}
	defer clear(values)
	endpoint, err := renderEndpoint(*target.Endpoint, values)
	if err != nil {
		return Endpoint{}, "", err
	}
	event.Secrets = values
	body, err := Render(target.Body, target.ContentType, event)
	return endpoint, body, err
}

// deliver never returns remote response bodies or transport errors containing URLs.
func (s *Service) deliver(ctx context.Context, target store.NotificationTarget, event Event) error {
	endpoint, body, err := s.render(target, event)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, target.Method, endpoint.URL, strings.NewReader(body))
	if err != nil {
		return errors.New("cannot create webhook request")
	}
	request.Header.Set("Content-Type", target.ContentType)
	request.Header.Set("User-Agent", "Piped-Webhook/1")
	for name, value := range endpoint.Headers {
		request.Header.Set(name, value)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return errors.New("webhook request failed; check endpoint, TLS, and connectivity")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", response.StatusCode)
	}
	return nil
}

// NotifyHost delivers once for each selected target after persisted host completion.
// Failures cannot change execution status; shutdown cancels pending requests.
func (s *Service) NotifyHost(ctx context.Context, logger *slog.Logger, runID, jobName string, connection store.Connection, status string) {
	if len(connection.Notifications) == 0 || ctx.Err() != nil {
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	output, err := s.db.NotificationOutput(lookup, runID, connection.Host.ID)
	cancel()
	if err != nil {
		logger.Error("notification output read failed", "run", runID, "host", connection.Host.ID)
		return
	}
	event := Event{Host: connection.Host.Name, Job: jobName, Status: status, Output: output}
	for _, target := range connection.Notifications {
		if ctx.Err() != nil {
			return
		}
		if err := s.deliver(ctx, target, event); err != nil {
			logger.Warn("webhook delivery failed", "run", runID, "host", connection.Host.ID, "target", target.ID, "error", err)
		}
	}
}
