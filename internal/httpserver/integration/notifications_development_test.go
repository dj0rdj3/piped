//go:build development

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"piped/internal/notifications"
	"piped/internal/store"
)

func TestNotificationDevelopmentScopesAndMCP(t *testing.T) {
	h := newHarness(t, false, true)
	_, readToken := h.issueKey(t, store.ScopeNotificationsRead)
	_, writeToken := h.issueKey(t, store.ScopeNotificationsWrite)
	secret := "fixture-private-value"
	input := notifications.Input{Type: "webhook", Secrets: []notifications.SecretInput{{Name: "secret", Value: &secret}}, Name: "Hook", Method: "POST", ContentType: "text/plain", Body: "$job: $status", Enabled: true, Endpoint: &notifications.Endpoint{URL: "https://example.internal/$secret"}}
	expect(t, h.bearerRequest("POST", "/api/development/v1/notifications", readToken, input, nil), 403)
	expect(t, h.bearerRequest("GET", "/api/development/v1/notifications", writeToken, nil, nil), 403)
	tools := h.mcpRequest(t, readToken, "tools/list", map[string]any{})
	encoded, _ := json.Marshal(tools)
	if !strings.Contains(string(encoded), "list_notification_targets") || strings.Contains(string(encoded), "save_notification_target") {
		t.Fatal("scope visibility incorrect")
	}
	saved := h.mcpRequest(t, writeToken, "tools/call", map[string]any{"name": "save_notification_target", "arguments": map[string]any{"settings": input}})
	encoded, _ = json.Marshal(saved)
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), `"isError":true`) {
		t.Fatalf("MCP save failed: %s", encoded)
	}
	listed := h.mcpRequest(t, readToken, "tools/call", map[string]any{"name": "list_notification_targets", "arguments": map[string]any{}})
	encoded, _ = json.Marshal(listed)
	if !strings.Contains(string(encoded), "Hook") || strings.Contains(string(encoded), secret) {
		t.Fatal("invalid MCP target read")
	}
}
