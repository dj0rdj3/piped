//go:build development

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"piped/internal/auth"
	"piped/internal/httpserver"
	"piped/internal/store"
)

func (h *harness) issueKey(t *testing.T, scopes ...string) (store.APIKey, string) {
	t.Helper()
	user, err := h.store.UserByName(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	key, token, err := auth.NewAPIKey(user, "Test development", scopes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.CreateLocalAPIKey(context.Background(), user, key, auth.TokenHash(token)); err != nil {
		t.Fatal(err)
	}
	return key, token
}

func (h *harness) bearerRequest(method, path, token string, body any, configure func(*http.Request)) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	r := httptest.NewRequest(method, h.origin+path, bytes.NewReader(encoded))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if configure != nil {
		configure(r)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func TestDevelopmentDisabledByDefault(t *testing.T) {
	h := newHarness(t, false)
	for _, path := range []string{"/api/v1/me", "/api/v1/users", "/api/development/keys", "/mcp"} {
		expect(t, h.bearerRequest("GET", path, "", nil, nil), 404)
	}
}

func TestDevelopmentMustStayOnLoopback(t *testing.T) {
	for _, config := range []httpserver.Config{
		{Listen: "0.0.0.0:8080", Origin: "https://localhost:8080", Development: true},
		{Listen: "127.0.0.1:8080", Origin: "https://piped.example", Development: true},
	} {
		if err := httpserver.ValidateConfig(config); err == nil {
			t.Fatal("development accepted network-facing configuration")
		}
	}
	h := newHarness(t, false, true)
	expect(t, h.request("GET", "/api-keys", nil, nil), 404)
}

func TestDevelopmentAuthenticationIsolationAndOrigin(t *testing.T) {
	h := newHarness(t, false, true)
	cookie := h.login(t, "admin", testPassword)
	_, token := h.issueKey(t, store.ScopeUsersRead)
	expect(t, h.request("GET", "/api/v1/users", nil, cookie), 401)
	expect(t, h.bearerRequest("GET", "/api/users", token, nil, nil), 401)
	expect(t, h.bearerRequest("GET", "/api/development/keys", token, nil, nil), 404)
	expect(t, h.bearerRequest("GET", "/api/v1/users?api_key="+token, "", nil, nil), 401)
	for _, path := range []string{"/api/v1/users", "/mcp"} {
		for _, modify := range []func(*http.Request){
			func(r *http.Request) { r.Host = "attacker.example" },
			func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") },
			func(r *http.Request) { r.Header.Set("Origin", "null") },
			func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		} {
			w := h.bearerRequest("GET", path, token, nil, modify)
			expect(t, w, 403)
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("cross-origin API enabled")
			}
		}
	}
	w := h.bearerRequest("GET", "/api/v1/users", token, nil, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+token) })
	expect(t, w, 401)
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing Bearer challenge")
	}
	expect(t, h.bearerRequest("GET", "/api/v1/users", token, nil, func(r *http.Request) { r.Header.Set("Authorization", "bearer "+token) }), 200)
	expect(t, h.bearerRequest("GET", "/api/v1/me", token, nil, nil), 200)
}

func TestSharedRoutesKeepBrowserAndBearerAuthoritySeparate(t *testing.T) {
	h := newHarness(t, false, true)
	cookie := h.login(t, "admin", testPassword)
	_, token := h.issueKey(t, store.ScopeUsersRead)
	for _, path := range []string{"/users", "/credentials", "/hosts", "/jobs", "/jobs/schedule-preview"} {
		expect(t, h.bearerRequest("GET", "/api"+path, token, nil, nil), 401)
		expect(t, h.request("GET", "/api/v1"+path, nil, cookie), 401)
	}
	for _, path := range []string{"/credentials", "/hosts", "/jobs", "/jobs/schedule-preview"} {
		expect(t, h.bearerRequest("GET", "/api/v1"+path, token, nil, nil), 403)
	}
	// Fixture deletion must stay off the browser API, and terminal sockets must
	// stay off the bearer API even when their shared execution routes are enabled.
	expect(t, h.request("DELETE", "/api/runs/missing", nil, cookie), 404)
	expect(t, h.bearerRequest("GET", "/api/v1/runs/missing/hosts/missing/terminal", token, nil, nil), 404)
}

func TestDevelopmentHasNoBrowserKeyManagement(t *testing.T) {
	h := newHarness(t, false, true)
	cookie := h.login(t, "admin", testPassword)
	_, token := h.issueKey(t, store.ScopeUsersRead)
	for _, path := range []string{"/api-keys", "/api-keys/", "/api/development/keys", "/api/development/keys/" + strings.Repeat("a", 32)} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			response := h.request(method, path, map[string]string{"current_password": testPassword}, cookie)
			// Unknown page routes reject writes with 405; API routes stay 404.
			expected := 404
			if strings.HasPrefix(path, "/api-keys") && method != "GET" {
				expected = 405
			}
			expect(t, response, expected)
			if method == "GET" {
				expect(t, h.bearerRequest(method, path, token, nil, nil), 404)
			}
		}
	}
}

func TestDevelopmentCredentialWritesAndLocalRevocation(t *testing.T) {
	h := newHarness(t, false, true)
	key, token := h.issueKey(t, store.ScopeCredentialsWrite)
	input := map[string]string{"mode": "generate", "name": "Integration"}
	w := h.bearerRequest("POST", "/api/v1/credentials", token, input, nil)
	expect(t, w, 201)
	if strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), "private_key") {
		t.Fatal("key creation exported private material")
	}
	var credential struct {
		Credential store.Credential `json:"credential"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &credential); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/credentials/" + credential.Credential.ID
	expect(t, h.bearerRequest("GET", "/api/v1/credentials", token, nil, nil), 403)
	expect(t, h.bearerRequest("PATCH", path, token, map[string]string{"name": "Renamed"}, nil), 204)
	expect(t, h.bearerRequest("DELETE", path, token, map[string]string{}, nil), 204)
	expect(t, h.bearerRequest("POST", "/api/v1/users", token, map[string]string{"username": "denied", "password": "long-password"}, nil), 403)
	if err := h.store.RevokeLocalAPIKey(context.Background(), key.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, h.bearerRequest("GET", "/api/v1/me", token, nil, nil), 401)
}

func TestDevelopmentUserWritesAndPasswordRevocation(t *testing.T) {
	h := newHarness(t, false, true)
	_, token := h.issueKey(t, store.ScopeUsersRead, store.ScopeUsersWrite)
	w := h.bearerRequest("POST", "/api/v1/users", token, map[string]string{"username": "other", "password": "long-password"}, nil)
	expect(t, w, 201)
	var created struct {
		User store.User `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/users/%d", created.User.ID)
	expect(t, h.bearerRequest("POST", path+"/password", token, map[string]string{"password": "new-long-password"}, nil), 204)
	expect(t, h.bearerRequest("DELETE", path, token, map[string]string{}, nil), 204)
	expect(t, h.bearerRequest("DELETE", "/api/v1/users/1", token, map[string]string{}, nil), 409)
	expect(t, h.bearerRequest("POST", "/api/v1/users/1/password", token, map[string]string{"password": "new-owner-password"}, nil), 204)
	expect(t, h.bearerRequest("GET", "/api/v1/users", token, nil, nil), 401)
}

func TestDevelopmentRateLimitIgnoresUntrustedForwardedHeaders(t *testing.T) {
	h := newHarness(t, false, true)
	for i := 0; i < 121; i++ {
		w := h.bearerRequest("GET", "/api/v1/me", "", nil, func(r *http.Request) { r.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d", i)) })
		want := 401
		if i == 120 {
			want = 429
			if w.Header().Get("Retry-After") != "60" {
				t.Fatal("missing retry guidance")
			}
		}
		expect(t, w, want)
	}
}

func TestDevelopmentRateLimitBehindTrustedProxy(t *testing.T) {
	for _, path := range []string{"/api/v1/me", "/mcp"} {
		t.Run(path, func(t *testing.T) {
			h := newHarness(t, false, true)
			for index := range 122 {
				w := h.bearerRequest("GET", path, "", nil, func(r *http.Request) {
					r.RemoteAddr = "127.0.0.1:1234"
					client := "203.0.113.5"
					if index == 121 {
						client = "203.0.113.6"
					}
					r.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d, %s", index, client))
				})
				want := 401
				if index == 120 {
					want = 429
				}
				expect(t, w, want)
			}
		})
	}
}

func TestMixedCredentialsCannotChangeRouteAuthority(t *testing.T) {
	h := newHarness(t, false, true)
	cookie := h.login(t, "admin", testPassword)
	_, token := h.issueKey(t, store.ScopeCredentialsRead)
	configure := func(r *http.Request) { r.AddCookie(cookie) }
	// A browser administrator session cannot fill in a bearer's missing scopes.
	expect(t, h.bearerRequest("GET", "/api/v1/users", token, nil, configure), 403)
	expect(t, h.bearerRequest("GET", "/api/v1/credentials", token, nil, configure), 200)
	// A bearer header neither replaces nor invalidates browser authentication.
	expect(t, h.bearerRequest("GET", "/api/users", "invalid", nil, configure), 200)
	if err := h.store.DeleteSession(context.Background(), auth.TokenHash(cookie.Value)); err != nil {
		t.Fatal(err)
	}
	expect(t, h.bearerRequest("GET", "/api/users", token, nil, configure), 401)
	expect(t, h.bearerRequest("GET", "/api/v1/credentials", token, nil, configure), 200)
}

func TestDevelopmentOriginAndHostGuardsStayRequestSpecific(t *testing.T) {
	h := newHarness(t, false, true)
	_, token := h.issueKey(t, store.ScopeCredentialsRead)
	for _, path := range []string{"/api/v1/credentials", "/mcp"} {
		for _, change := range []func(*http.Request){
			func(r *http.Request) { r.Host = "attacker.example" },
			func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:9999") },
			func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		} {
			// Exercise valid requests between invalid ones to catch cached request
			// state accidentally replacing immutable configuration state.
			expect(t, h.bearerRequest("GET", "/api/v1/credentials", token, nil, nil), 200)
			expect(t, h.bearerRequest("GET", path, token, nil, change), 403)
		}
	}
	for _, path := range []string{"/api/v1x/credentials", "/api/v10/credentials", "/api/v1"} {
		w := h.bearerRequest("POST", path, token, map[string]string{}, nil)
		expect(t, w, 403)
	}
}
