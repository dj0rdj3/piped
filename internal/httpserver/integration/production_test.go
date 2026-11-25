//go:build !development

package integration

import (
	"testing"

	"piped/internal/httpserver"
)

func TestProductionCannotEnableDevelopmentIntegrations(t *testing.T) {
	if err := httpserver.ValidateConfig(httpserver.Config{Listen: "127.0.0.1:8080", Origin: "http://127.0.0.1:8080", Development: true}); err == nil {
		t.Fatal("production build accepted development configuration")
	}
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	for _, path := range []string{
		"/api/development/v1/users", "/api/development/v1/me", "/api/development/v1/credentials",
		"/api/development/v1/notifications", "/api/development/v1/jobs", "/api/development/v1/hosts", "/api/development/v1/runs/missing",
		"/api/development/keys", "/mcp", "/mcp/",
	} {
		for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			response := h.request(method, path, map[string]string{}, cookie)
			if response.Code != 404 && response.Code != 405 {
				t.Fatalf("production exposed %s %s: HTTP %d", method, path, response.Code)
			}
		}
	}
}
