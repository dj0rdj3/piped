//go:build development

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"piped/internal/store"
)

func TestExecutionDevelopmentScopesAndToolDiscovery(t *testing.T) {
	h := newHarness(t, false, true)
	_, authorToken := h.issueKey(t, store.ScopeJobsRead, store.ScopeJobsWrite)
	encoded, _ := json.Marshal(h.mcpRequest(t, authorToken, "tools/list", map[string]any{}))
	if strings.Contains(string(encoded), `"run_job"`) {
		t.Fatal("definition-author key received execution capability")
	}
	expect(t, h.bearerRequest("POST", "/api/v1/jobs/missing/run", authorToken, map[string]int{"version": 1}, nil), 403)
	_, readToken := h.issueKey(t, store.ScopeRunsRead)
	encoded, _ = json.Marshal(h.mcpRequest(t, readToken, "tools/list", map[string]any{}))
	for _, name := range []string{"list_runs", "get_run", "read_run_output"} {
		if !strings.Contains(string(encoded), name) {
			t.Fatalf("missing read tool %s", name)
		}
	}
	if strings.Contains(string(encoded), `"run_job"`) {
		t.Fatal("read key received execution mutation")
	}
	expect(t, h.bearerRequest("POST", "/api/v1/runs/missing/hosts/missing/input", readToken, map[string]string{"data": "danger"}, nil), 403)
	_, writeToken := h.issueKey(t, store.ScopeRunsWrite)
	encoded, _ = json.Marshal(h.mcpRequest(t, writeToken, "tools/list", map[string]any{}))
	for _, name := range []string{"run_job", "cancel_run_host", "send_run_input", "approve_run_host_key"} {
		if !strings.Contains(string(encoded), name) {
			t.Fatalf("missing write tool %s", name)
		}
	}
	expect(t, h.bearerRequest("GET", "/api/v1/runs/missing", writeToken, nil, nil), 403)
	expect(t, h.bearerRequest("POST", "/api/v1/jobs/missing/run", writeToken, map[string]int{"version": 1}, nil), 404)
	expect(t, h.bearerRequest("GET", "/api/v1/runs/missing", readToken, nil, nil), 404)
	expect(t, h.bearerRequest("DELETE", "/api/v1/runs/missing", readToken, nil, nil), 403)
}
