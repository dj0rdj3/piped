package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"piped/internal/auth"
	"piped/internal/httpserver"
)

func TestWorkspaceDirectEntryRoutes(t *testing.T) {
	h := newHarness(t, false)
	for _, path := range []string{"/", "/login", "/login/", "/login?next=%2Fcredentials", "/users", "/users/", "/credentials", "/credentials/", "/hosts", "/hosts/", "/hosts?from=bookmark", "/jobs", "/jobs/", "/jobs?from=bookmark", "/users?from=bookmark", "/credentials?from=bookmark"} {
		for _, method := range []string{"GET", "HEAD"} {
			r := httptest.NewRequest(method, path, nil)
			w := httptest.NewRecorder()
			h.handler.ServeHTTP(w, r)
			expect(t, w, 200)
			if method == "GET" && w.Body.String() != "piped frontend" {
				t.Fatalf("wrong SPA document at %s", path)
			}
			if method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD returned a body")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("SPA entry document is cacheable")
			}
			if r.URL.Path != strings.Split(path, "?")[0] {
				t.Fatal("handler mutated caller's URL")
			}
		}
	}
	for _, path := range []string{"/missing", "/users/missing", "/credentials/key", "/hosts/missing", "/jobs/missing", "/assets/missing.js", "/api/missing", "/.env", "/internal/store/store.go"} {
		w := h.request("GET", path, nil, nil)
		expect(t, w, 404)
		if strings.Contains(w.Body.String(), "piped frontend") {
			t.Fatalf("missing resource received SPA: %s", path)
		}
	}
	for _, path := range []string{"/login", "/users", "/credentials", "/hosts", "/jobs"} {
		expect(t, h.request("POST", path, nil, nil), 405)
	}
}

func TestRoutingAndAuthenticationFailuresKeepSecurityHeaders(t *testing.T) {
	h := newHarness(t, true)
	cookie := h.login(t, "admin", testPassword)
	for _, fixture := range []struct {
		method, path  string
		status        int
		authenticated bool
	}{
		{"GET", "/api/health", 200, false},
		{"HEAD", "/api/health", 200, false},
		{"GET", "/api/auth/me", 200, true},
		{"GET", "/api/auth/me", 401, false},
		{"GET", "/api/users", 200, true},
		{"GET", "/api/users", 401, false},
		{"POST", "/api/auth/me", 404, true},
		{"GET", "/api/missing", 404, true},
		{"GET", "/jobs", 200, false},
		{"HEAD", "/jobs", 200, false},
		{"GET", "/missing", 404, false},
		{"POST", "/jobs", 405, true},
		{"GET", "/index.html", 301, false},
		{"GET", "/api//auth/me", 307, false},
	} {
		t.Run(fixture.method+" "+fixture.path, func(t *testing.T) {
			var session *http.Cookie
			if fixture.authenticated {
				session = cookie
			}
			w := h.request(fixture.method, fixture.path, nil, session)
			expect(t, w, fixture.status)
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Frame-Options") != "DENY" {
				t.Fatal("routing or authentication failure bypassed security headers")
			}
			if !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self' wss://piped.example;") || w.Header().Get("Strict-Transport-Security") == "" {
				t.Fatal("routing changed the configured TLS/WebSocket security policy")
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("routing granted cross-origin access")
			}
		})
	}
	// Neither a request Host nor forwarded headers may redefine the CSP origin.
	request := httptest.NewRequest("GET", h.origin+"/api/health", nil)
	request.Host = "other.example"
	request.Header.Set("X-Forwarded-Host", "other.example")
	request.Header.Set("X-Forwarded-Proto", "http")
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, request)
	expect(t, w, 200)
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "wss://piped.example") || strings.Contains(w.Header().Get("Content-Security-Policy"), "other.example") {
		t.Fatal("request headers changed the configured security policy")
	}
	if err := h.store.DeleteSession(context.Background(), auth.TokenHash(cookie.Value)); err != nil {
		t.Fatal(err)
	}
	w = h.request("GET", "/api/auth/me", nil, cookie)
	expect(t, w, 401)
	cleared := w.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != "__Host-piped_session" || !cleared[0].Secure || !cleared[0].HttpOnly || cleared[0].MaxAge != -1 {
		t.Fatal("expired-session response did not clear the protected cookie")
	}
}

func TestAbsentOptionalServicesDoNotRegisterRoutes(t *testing.T) {
	h := newHarness(t, false)
	minimal, err := httpserver.New(h.auth, httpserver.Config{Listen: "127.0.0.1:8080", Origin: h.origin})
	if err != nil {
		t.Fatal(err)
	}
	h.handler = minimal
	cookie := h.login(t, "admin", testPassword)
	expect(t, h.request("GET", "/api/users", nil, cookie), 200)
	for _, path := range []string{"/api/credentials", "/api/hosts", "/api/jobs", "/api/jobs/schedule-preview", "/api/jobs/missing/run", "/api/runs/missing", "/api/runs/missing/hosts/missing/terminal", "/api/v1/users", "/mcp"} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			w := h.request(method, path, nil, cookie)
			if w.Code != 404 && w.Code != 405 {
				t.Fatalf("unavailable service registered %s %s: %d", method, path, w.Code)
			}
		}
	}
}
