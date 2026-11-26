package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserAndAssetsRejectUntrustedRequestHosts(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, secure)
			cookie := h.login(t, "admin", testPassword)
			for _, endpoint := range []struct{ method, path string }{
				{"GET", "/"},
				{"HEAD", "/assets/app.js"},
				{"GET", "/api/auth/me"},
				{"GET", "/api/users"},
				{"POST", "/api/auth/login"},
				{"POST", "/api/auth/logout"},
			} {
				for _, host := range []string{"attacker.example", "attacker.example:8080", "127.0.0.1:9999", ""} {
					t.Run(endpoint.method+endpoint.path+"/"+host, func(t *testing.T) {
						body, err := json.Marshal(map[string]string{"username": "admin", "password": testPassword})
						if err != nil {
							t.Fatal(err)
						}
						request := httptest.NewRequest(endpoint.method, h.origin+endpoint.path, bytes.NewReader(body))
						request.Host = host
						request.AddCookie(cookie)
						request.Header.Set("Content-Type", "application/json")
						request.Header.Set("X-Piped-Request", "1")
						request.Header.Set("Origin", h.origin)
						request.Header.Set("Sec-Fetch-Site", "same-origin")
						// Forwarding metadata must never replace the actual Host.
						request.Header.Set("X-Forwarded-Host", strings.TrimPrefix(h.origin, name+"://"))
						request.Header.Set("Forwarded", "host="+strings.TrimPrefix(h.origin, name+"://"))
						response := httptest.NewRecorder()
						h.handler.ServeHTTP(response, request)
						expect(t, response, http.StatusForbidden)
						if response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
							t.Fatal("untrusted host changed session cookies or received a cacheable response")
						}
					})
				}
			}
			// Rejected logout requests must not revoke a valid session.
			expect(t, h.request("GET", "/api/auth/me", nil, cookie), http.StatusOK)
			for _, host := range []string{strings.TrimPrefix(h.origin, name+"://"), "127.0.0.1:8080"} {
				request := httptest.NewRequest("GET", h.origin+"/api/auth/me", nil)
				request.Host = strings.ToUpper(host)
				request.AddCookie(cookie)
				response := httptest.NewRecorder()
				h.handler.ServeHTTP(response, request)
				expect(t, response, http.StatusOK)
			}
		})
	}
}
