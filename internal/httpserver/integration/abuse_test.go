package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"piped/internal/store"
)

func TestBrowserMutationAbuseCannotBypassCSRFOrPasswordConfirmation(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	body, _ := json.Marshal(map[string]any{"username": "abuse-created", "password": testPassword, "current_password": testPassword})
	for _, attack := range []struct{ name, origin, site, header string }{
		{"simple-form", "https://evil.example", "cross-site", ""},
		{"forged-custom-header", "https://evil.example", "cross-site", "1"},
		{"opaque-origin", "null", "same-origin", "1"},
		{"missing-custom-header", h.origin, "same-origin", ""},
		{"same-site-other-port", "http://127.0.0.1:9999", "same-site", "1"},
	} {
		t.Run(attack.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", h.origin+"/api/users", bytes.NewReader(body))
			r.AddCookie(cookie)
			r.Header.Set("Content-Type", "application/json")
			if attack.origin != "" {
				r.Header.Set("Origin", attack.origin)
			}
			if attack.site != "" {
				r.Header.Set("Sec-Fetch-Site", attack.site)
			}
			if attack.header != "" {
				r.Header.Set("X-Piped-Request", attack.header)
			}
			w := httptest.NewRecorder()
			h.handler.ServeHTTP(w, r)
			expect(t, w, 403)
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("attack received CORS permission")
			}
		})
	}
	_, token := h.createPersonalKey(t, cookie, store.ScopeUsersWrite)
	// A bearer header cannot convert cookie-based key management into a bearer
	// mutation or bypass the owner's current-password check.
	request := httptest.NewRequest("POST", h.origin+"/api/api-keys", strings.NewReader(`{"name":"forged","scopes":["users:write"],"expires_in_hours":24,"current_password":"wrong"}`))
	request.AddCookie(cookie)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Piped-Request", "1")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	expect(t, response, 401)
	for _, input := range []map[string]any{
		{"username": "forged-actor", "password": testPassword, "current_password": testPassword, "actor": map[string]any{"id": 1}},
		{"username": "forged-hash", "password": testPassword, "current_password": testPassword, "password_hash": "chosen-hash"},
	} {
		expect(t, h.request("POST", "/api/users", input, cookie), 400)
	}
	users := h.request("GET", "/api/users", nil, cookie)
	expect(t, users, 200)
	if strings.Contains(users.Body.String(), "abuse-created") || strings.Contains(users.Body.String(), "forged-") {
		t.Fatal("browser abuse created an administrator")
	}
}

func TestPublicAPIAbuseCannotEscapeReadOnlyScope(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	_, token := h.createPersonalKey(t, cookie, store.ScopeCredentialsRead)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		path := "/api/v1/credentials"
		if method != "POST" {
			path += "/" + strings.Repeat("a", 32)
		}
		response := h.bearerRequest(method, path, token, map[string]any{"name": "forbidden", "mode": "generate"}, nil)
		if response.Code != 403 && response.Code != 404 {
			t.Fatalf("read-only key reached mutation: %s %d", method, response.Code)
		}
	}
	for _, path := range []string{"/api/v1/hosts", "/api/v1/jobs", "/api/v1/users", "/api/v1/runtime"} {
		expect(t, h.bearerRequest("GET", path, token, nil, nil), 403)
	}
	expect(t, h.bearerRequest("POST", "/api/api-keys", token, map[string]any{"name": "forbidden", "scopes": []string{store.ScopeUsersWrite}}, func(r *http.Request) { r.Header.Set("X-Piped-Request", "1") }), 401)
	expect(t, h.bearerRequest("POST", "/api/v1/api-keys", token, map[string]any{}, nil), 404)
	// Override headers are data; they cannot turn a permitted read into a delete.
	expect(t, h.bearerRequest("GET", "/api/v1/credentials", token, nil, func(r *http.Request) { r.Header.Set("X-HTTP-Method-Override", "DELETE") }), 200)
	for _, query := range []string{"limit=1&q=%27%20OR%201%3D1--", "limit=1&ids=%27%3BDROP%20TABLE%20users%3B--", "limit=1&cursor=eyJpZCI6ImEifQ", "limit=9223372036854775807"} {
		response := h.bearerRequest("GET", "/api/v1/credentials?"+query, token, nil, nil)
		if response.Code != 200 && response.Code != 400 {
			t.Fatal("query abuse reached an internal failure", response.Code)
		}
	}
	expect(t, h.request("GET", "/api/users", nil, cookie), 200)
	for _, path := range []string{"/.env", "/%2eenv", "/data/piped.db", "/api/v1/../../.env", "/api/v1/%2e%2e/%2e%2e/.env"} {
		response := h.bearerRequest("GET", path, token, nil, nil)
		if response.Code == 200 || strings.Contains(response.Body.String(), "PIPED_ENCRYPTION_KEY") {
			t.Fatal("path abuse exposed private material", path)
		}
	}
}

func TestPublicAPIFloodCannotEvadePerKeyLimitWithForwardedIPs(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	_, token := h.createPersonalKey(t, cookie, store.ScopeUsersRead)
	for index := range 120 {
		response := h.bearerRequest("GET", "/api/v1/me", token, nil, func(r *http.Request) {
			r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", index))
		})
		expect(t, response, 200)
	}
	response := h.bearerRequest("GET", "/api/v1/me", token, nil, func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.250") })
	expect(t, response, 429)
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limit response omitted retry guidance")
	}
}
