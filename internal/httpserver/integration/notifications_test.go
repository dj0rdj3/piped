package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"piped/internal/notifications"
	"piped/internal/store"
)

func TestNotificationRoutesReadableTemplatesAndWriteOnlySecrets(t *testing.T) {
	h := newHarness(t, false)
	secret := "fixture-private-value"
	input := notifications.Input{Type: "webhook", Secrets: []notifications.SecretInput{{Name: "secret", Value: &secret}}, Name: "Chat", Method: "POST", ContentType: "application/json", Body: `{"text":"$job on $host: $status\n$output:10"}`, Enabled: true, DefaultEnabled: true, Endpoint: &notifications.Endpoint{URL: "https://example.internal/hook?token=$secret", Headers: map[string]string{"Authorization": "Bearer $secret"}}}
	expect(t, h.request("POST", "/api/notifications", input, nil), 401)
	cookie := h.login(t, "admin", testPassword)
	created := h.request("POST", "/api/notifications", input, cookie)
	expect(t, created, 201)
	var result struct {
		Target store.NotificationTarget `json:"target"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Target.Type != "webhook" {
		t.Fatal("target type was not saved")
	}
	for _, secrets := range [][]notifications.SecretInput{
		{{Name: "HOST", Value: &secret}},
		{{Name: "Token", Value: &secret}, {Name: "TOKEN", Value: &secret}},
	} {
		invalid := input
		invalid.Secrets = secrets
		expect(t, h.request("POST", "/api/notifications", invalid, cookie), 400)
	}
	unsupported := input
	unsupported.Type = ""
	expect(t, h.request("POST", "/api/notifications", unsupported, cookie), 400)
	unsupported.Type = "email"
	expect(t, h.request("POST", "/api/notifications", unsupported, cookie), 400)
	for _, body := range []string{created.Body.String(), h.request("GET", "/api/notifications", nil, cookie).Body.String()} {
		if strings.Contains(body, secret) || !strings.Contains(body, "$secret") || !strings.Contains(body, "endpoint") {
			t.Fatal("invalid endpoint template or secret exposure")
		}
	}
	input.Version, input.Endpoint, input.Name, input.Secrets = result.Target.Version, nil, "Renamed", nil
	expect(t, h.request("PUT", "/api/notifications/"+result.Target.ID, input, cookie), 200)
	expect(t, h.request("PUT", "/api/notifications/"+result.Target.ID, input, cookie), 409)
	input.Version++
	for _, body := range []string{`{"text":"$unknown"}`, `[1$output]`} {
		input.Body = body
		expect(t, h.request("PUT", "/api/notifications/"+result.Target.ID, input, cookie), 400)
	}
	expect(t, h.request("DELETE", "/api/notifications/"+result.Target.ID, map[string]any{"version": 1}, cookie), 409)
	expect(t, h.request("DELETE", "/api/notifications/"+result.Target.ID, map[string]any{"version": 2}, cookie), 204)
	expect(t, h.request("GET", "/notifications", nil, cookie), 200)
}

func TestProductionNotificationScopes(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	_, readToken := h.createPersonalKey(t, cookie, store.ScopeNotificationsRead)
	_, writeToken := h.createPersonalKey(t, cookie, store.ScopeNotificationsWrite)
	secret := "fixture-private-value"
	input := notifications.Input{Type: "webhook", Secrets: []notifications.SecretInput{{Name: "secret", Value: &secret}}, Name: "Production hook", Method: "POST", ContentType: "text/plain", Body: "$job: $status", Enabled: true, Endpoint: &notifications.Endpoint{URL: "https://example.internal/$secret"}}
	expect(t, h.bearerRequest("POST", "/api/v1/notifications", readToken, input, nil), 403)
	expect(t, h.bearerRequest("GET", "/api/v1/notifications", writeToken, nil, nil), 403)
	expect(t, h.bearerRequest("POST", "/api/v1/notifications", writeToken, input, nil), 201)
	result := h.bearerRequest("GET", "/api/v1/notifications", readToken, nil, nil)
	expect(t, result, 200)
	if !strings.Contains(result.Body.String(), "Production hook") || strings.Contains(result.Body.String(), secret) || !strings.Contains(result.Body.String(), "$secret") {
		t.Fatal("invalid production target metadata")
	}
	expect(t, h.request("GET", "/api/v1/notifications", nil, cookie), 401)
}
