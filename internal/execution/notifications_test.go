package execution

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/jobs"
	"piped/internal/notifications"
	"piped/internal/store"
)

func TestCompletionWebhookSnapshotMaskedOutputAndFailureIsolation(t *testing.T) {
	f := newFixture(t, "sequential", false)
	ctx := context.Background()
	received := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/notification-token" || r.Header.Get("Authorization") != "Bearer notification-token" {
			t.Error("notification secret snapshot changed")
		}
		data, _ := io.ReadAll(r.Body)
		received <- string(data)
		w.WriteHeader(503) // Delivery failure must not change host/run status.
	}))
	defer server.Close()
	service, err := notifications.New(ctx, f.db, f.vault)
	if err != nil {
		t.Fatal(err)
	}
	notificationToken := "notification-token"
	target, err := service.Save(ctx, f.actor, "", notifications.Input{Type: "webhook", Secrets: []notifications.SecretInput{{Name: "token", Value: &notificationToken}}, Name: "Hook", Method: "POST", ContentType: "text/plain", Body: "$host|$job|$status|$output:1|$token", Enabled: true, Endpoint: &notifications.Endpoint{URL: server.URL + "/$token", Headers: map[string]string{"Authorization": "Bearer $token"}}})
	if err != nil {
		t.Fatal(err)
	}
	secret := "fixture-job-secret"
	f.job, err = jobService(t, f.db, f.vault).Save(ctx, f.actor, f.job.ID, jobs.Input{Name: f.job.Name, Script: f.job.Script, HostIDs: f.job.HostIDs, ExecutionMode: f.job.ExecutionMode, Version: f.job.Version, NotificationTargetIDs: []string{target.ID}, Secrets: []jobs.SecretInput{{Name: "TOKEN", Value: &secret}}})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.engine.runner = func(ctx context.Context, c store.Connection, _ string, output io.Writer, _ func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		_, _ = io.WriteString(output, "old\n"+secret+"\n")
		code := 0
		if c.Host.ID == f.hosts[1].ID {
			code = 1
			return &code, errors.New("exit 1")
		}
		return &code, nil
	}
	run := startFixture(t, f)
	<-started
	// Disable and redirect after admission; this run must keep its original target.
	replacement := "replacement-token"
	_, err = service.Save(ctx, f.actor, target.ID, notifications.Input{Type: "webhook", Secrets: []notifications.SecretInput{{Name: "token", Value: &replacement}}, Name: "Changed", Method: "POST", ContentType: "text/plain", Body: "changed", Version: target.Version, Enabled: false, Endpoint: &notifications.Endpoint{URL: "http://127.0.0.1:1/should-not-deliver"}})
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	result := finished(t, f, run.ID)
	if result.Status != "failed" || result.Hosts[0].Status != "succeeded" || result.Hosts[1].Status != "failed" {
		t.Fatalf("webhook changed execution: %+v", result)
	}
	for range 3 {
		select {
		case data := <-received:
			if strings.Contains(data, secret) || strings.Contains(data, "old") || strings.Contains(data, "changed") || !strings.Contains(data, f.job.Name) || !strings.Contains(data, "[REDACTED]") || !strings.Contains(data, notificationToken) {
				t.Fatalf("invalid completion payload: %q", data)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("completion webhook missing")
		}
	}
}
