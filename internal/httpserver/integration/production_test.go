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
		"/api/v1/hosts", "/api/v1/jobs", "/api/v1/credentials", "/api/v1/users", "/api/v1/me",
		"/api/v1/jobs/missing/run", "/api/v1/runs/missing", "/api/v1/jobs/schedule-preview",
		"/api/v1/runs/missing/hosts/missing/input", "/api/v1/runs/missing/hosts/missing/cancel",
		"/api/development/keys", "/mcp", "/mcp/", "/api-keys", "/api-keys/",
	} {
		for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			response := h.request(method, path, map[string]string{}, cookie)
			if response.Code != 404 && response.Code != 405 {
				t.Fatalf("production exposed %s %s: HTTP %d", method, path, response.Code)
			}
		}
	}
}
