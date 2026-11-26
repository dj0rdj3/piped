package integration

import (
	"strings"
	"testing"

	"piped/internal/store"
)

func TestReadinessAndScopedRuntimeDiagnostics(t *testing.T) {
	h := newHarness(t, false)
	expect(t, h.request("GET", "/api/health", nil, nil), 200)
	ready := h.request("GET", "/api/ready", nil, nil)
	expect(t, ready, 200)
	if strings.Contains(ready.Body.String(), "active_") {
		t.Fatal("anonymous probe disclosed diagnostics")
	}
	expect(t, h.request("GET", "/api/runtime", nil, nil), 401)
	cookie := h.login(t, "admin", testPassword)
	_, denied := h.createPersonalKey(t, cookie, store.ScopeJobsRead)
	expect(t, h.bearerRequest("GET", "/api/v1/runtime", denied, nil, nil), 403)
	_, allowed := h.createPersonalKey(t, cookie, store.ScopeRunsRead)
	status := h.bearerRequest("GET", "/api/v1/runtime", allowed, nil, nil)
	expect(t, status, 200)
	for _, field := range []string{"active_scripts", "pending_results", "retrying_persistence", "ownership_lost", "shutdown"} {
		if !strings.Contains(status.Body.String(), `"`+field+`"`) {
			t.Fatal("missing aggregate runtime field", field)
		}
	}
	for _, private := range []string{"username", "host_id", "password", "script\"", "address", "dsn"} {
		if strings.Contains(status.Body.String(), private) {
			t.Fatal("runtime diagnostics exposed private data", private)
		}
	}
	h.executions.Close()
	expect(t, h.request("GET", "/api/ready", nil, nil), 503)
	expect(t, h.request("GET", "/api/health", nil, nil), 200)
}
