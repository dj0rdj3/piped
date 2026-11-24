//go:build development

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"piped/internal/store"
)

func TestJobDevelopmentAPIAndMCP(t *testing.T) {
	h := newHarness(t, false, true)
	_, token := h.issueKey(t, store.ScopeHostsWrite, store.ScopeJobsRead, store.ScopeJobsWrite)
	_, readToken := h.issueKey(t, store.ScopeJobsRead)
	_, unrelatedToken := h.issueKey(t, store.ScopeUsersRead)
	w := h.bearerRequest("POST", "/api/v1/hosts", token, hostBody(), nil)
	expect(t, w, 201)
	host := responseHost(t, w)
	input := jobBody(host.ID)
	input["secrets"] = []map[string]any{{"name": "API_TOKEN", "value": "mcp-sensitive-job-value"}}
	expect(t, h.bearerRequest("POST", "/api/v1/jobs", readToken, input, nil), 403)
	expect(t, h.bearerRequest("GET", "/api/v1/jobs", unrelatedToken, nil, nil), 403)
	w = h.bearerRequest("POST", "/api/v1/jobs", token, input, nil)
	expect(t, w, 201)
	job := responseJob(t, w)
	if strings.Contains(w.Body.String(), "mcp-sensitive-job-value") || len(job.SecretNames) != 1 {
		t.Fatal("API secret readback")
	}
	schemaFailure := h.mcpRequest(t, token, "tools/call", map[string]any{"name": "save_job", "arguments": map[string]any{
		"name": "Invalid", "script": "echo test", "host_ids": []string{host.ID}, "execution_mode": "sequential", "stop_on_failure": false,
		"secrets": []map[string]any{{"name": "API_TOKEN", "value": []string{"schema-sensitive-secret-value"}}},
	}})
	schemaEncoded, _ := json.Marshal(schemaFailure)
	if strings.Contains(string(schemaEncoded), "schema-sensitive-secret-value") {
		t.Fatal("MCP schema error exposed secret argument")
	}
	tools := h.mcpRequest(t, token, "tools/list", map[string]any{})
	encoded, _ := json.Marshal(tools)
	for _, name := range []string{"list_jobs", "save_job", "delete_job"} {
		if !strings.Contains(string(encoded), name) {
			t.Fatalf("missing MCP tool %s", name)
		}
	}
	readTools := h.mcpRequest(t, readToken, "tools/list", map[string]any{})
	encoded, _ = json.Marshal(readTools)
	if strings.Contains(string(encoded), "save_job") || strings.Contains(string(encoded), "delete_job") || !strings.Contains(string(encoded), "list_jobs") {
		t.Fatal("MCP scope filtering failed")
	}
	result := h.mcpRequest(t, token, "tools/call", map[string]any{"name": "list_jobs", "arguments": map[string]any{}})
	encoded, _ = json.Marshal(result)
	if !strings.Contains(string(encoded), job.ID) {
		t.Fatal("MCP list missing job")
	}
	input["secrets"] = []map[string]any{{"name": "API_TOKEN"}}
	input["id"], input["version"], input["name"] = job.ID, job.Version, "MCP updated job"
	result = h.mcpRequest(t, token, "tools/call", map[string]any{"name": "save_job", "arguments": input})
	encoded, _ = json.Marshal(result)
	if strings.Contains(string(encoded), "mcp-sensitive-job-value") || strings.Contains(string(encoded), `"isError":true`) || !strings.Contains(string(encoded), `"run_as_sudo":true`) || !strings.Contains(string(encoded), "MCP updated job") {
		t.Fatalf("MCP save failed: %s", encoded)
	}
	expect(t, h.bearerRequest("PUT", "/api/v1/jobs/"+job.ID, token, jobBody(host.ID), nil), 400)
	expect(t, h.bearerRequest("DELETE", "/api/v1/hosts/"+host.ID, token, map[string]any{"version": 1}, nil), 409)
	result = h.mcpRequest(t, token, "tools/call", map[string]any{"name": "delete_job", "arguments": map[string]any{"id": job.ID, "version": 2}})
	encoded, _ = json.Marshal(result)
	if !strings.Contains(string(encoded), `"removed":true`) {
		t.Fatalf("MCP delete failed: %s", encoded)
	}
	// Also exercise MCP creation and API update/delete.
	delete(input, "id")
	delete(input, "version")
	input["secrets"] = []map[string]any{{"name": "API_TOKEN", "value": "mcp-sensitive-job-value"}}
	result = h.mcpRequest(t, token, "tools/call", map[string]any{"name": "save_job", "arguments": input})
	encoded, _ = json.Marshal(result)
	if strings.Contains(string(encoded), `"isError":true`) {
		t.Fatalf("MCP create failed: %s", encoded)
	}
	w = h.bearerRequest("GET", "/api/v1/jobs", readToken, nil, nil)
	expect(t, w, 200)
	var listed struct {
		Jobs []store.Job `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Jobs) != 1 {
		t.Fatalf("list failed: %v", err)
	}
	created := listed.Jobs[0]
	input["version"], input["name"] = created.Version, "API updated job"
	w = h.bearerRequest("PUT", "/api/v1/jobs/"+created.ID, token, input, nil)
	expect(t, w, 200)
	expect(t, h.bearerRequest("DELETE", "/api/v1/jobs/"+created.ID, token, map[string]any{"version": 2}, nil), 204)
	expect(t, h.bearerRequest("DELETE", "/api/v1/hosts/"+host.ID, token, map[string]any{"version": 1}, nil), 204)
	key, revokedToken := h.issueKey(t, store.ScopeJobsWrite)
	if err := h.store.RevokeLocalAPIKey(context.Background(), key.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, h.bearerRequest("POST", "/api/v1/jobs", revokedToken, input, nil), 401)
}

func TestScheduleAPIAndMCPScopeChecks(t *testing.T) {
	h := newHarness(t, false, true)
	_, editor := h.issueKey(t, store.ScopeHostsWrite, store.ScopeJobsWrite, store.ScopeJobsRead)
	_, executor := h.issueKey(t, store.ScopeJobsWrite, store.ScopeJobsRead, store.ScopeRunsWrite)
	hostResponse := h.bearerRequest("POST", "/api/v1/hosts", editor, hostBody(), nil)
	expect(t, hostResponse, 201)
	input := jobBody(responseHost(t, hostResponse).ID)
	input["schedule"] = map[string]any{"cron": "0 2 * * *", "enabled": true}
	expect(t, h.bearerRequest("POST", "/api/v1/jobs", editor, input, nil), 403)
	w := h.bearerRequest("POST", "/api/v1/jobs", executor, input, nil)
	expect(t, w, 201)
	job := responseJob(t, w)
	if !job.Schedule.Enabled || job.Schedule.Cron != "0 2 * * *" {
		t.Fatal("API did not save schedule")
	}
	input["id"], input["version"] = job.ID, job.Version
	delete(input, "schedule")
	result := h.mcpRequest(t, editor, "tools/call", map[string]any{"name": "save_job", "arguments": input})
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"isError":true`) {
		t.Fatal("MCP changed scheduled script without execution scope")
	}
	input["schedule"] = map[string]any{"cron": "0 2 * * *", "enabled": false}
	result = h.mcpRequest(t, editor, "tools/call", map[string]any{"name": "save_job", "arguments": input})
	encoded, _ = json.Marshal(result)
	if strings.Contains(string(encoded), `"isError":true`) || !strings.Contains(string(encoded), `"enabled":false`) {
		t.Fatalf("MCP pause failed: %s", encoded)
	}
	result = h.mcpRequest(t, editor, "tools/call", map[string]any{"name": "list_jobs", "arguments": map[string]any{}})
	encoded, _ = json.Marshal(result)
	if !strings.Contains(string(encoded), `"server_timezone"`) {
		t.Fatal("MCP timezone missing")
	}
	w = h.bearerRequest("GET", "/api/v1/jobs", editor, nil, nil)
	expect(t, w, 200)
	var response struct {
		ServerTimezone string `json:"server_timezone"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.ServerTimezone == "" {
		t.Fatal("API timezone missing")
	}
}

func TestJobCopyAndPreviewDevelopmentTools(t *testing.T) {
	h := newHarness(t, false, true)
	_, editor := h.issueKey(t, store.ScopeHostsWrite, store.ScopeJobsRead, store.ScopeJobsWrite)
	_, writer := h.issueKey(t, store.ScopeJobsWrite)
	_, reader := h.issueKey(t, store.ScopeJobsRead)
	hostResponse := h.bearerRequest("POST", "/api/v1/hosts", editor, hostBody(), nil)
	expect(t, hostResponse, 201)
	input := jobBody(responseHost(t, hostResponse).ID)
	input["secrets"] = []map[string]any{{"name": "TOKEN", "value": "development-copy-sensitive"}}
	response := h.bearerRequest("POST", "/api/v1/jobs", editor, input, nil)
	expect(t, response, 201)
	source := responseJob(t, response)
	input["copy_from"] = map[string]any{"id": source.ID, "version": source.Version}
	input["name"] = "API copy"
	delete(input, "secrets")
	expect(t, h.bearerRequest("POST", "/api/v1/jobs", writer, input, nil), 403)
	response = h.bearerRequest("POST", "/api/v1/jobs", editor, input, nil)
	expect(t, response, 201)
	if len(responseJob(t, response).SecretNames) != 1 || strings.Contains(response.Body.String(), "development-copy-sensitive") {
		t.Fatal("API copy exposed or lost secret")
	}
	input["name"] = "MCP copy"
	result := h.mcpRequest(t, editor, "tools/call", map[string]any{"name": "save_job", "arguments": input})
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), `"isError":true`) || strings.Contains(string(encoded), "development-copy-sensitive") || !strings.Contains(string(encoded), `"TOKEN"`) {
		t.Fatalf("MCP copy failed: %s", encoded)
	}
	expect(t, h.bearerRequest("GET", "/api/v1/jobs/schedule-preview?cron=*%20*%20*%20*%20*", writer, nil, nil), 403)
	response = h.bearerRequest("GET", "/api/v1/jobs/schedule-preview?cron=*%20*%20*%20*%20*", reader, nil, nil)
	expect(t, response, 200)
	result = h.mcpRequest(t, reader, "tools/call", map[string]any{"name": "preview_schedule", "arguments": map[string]any{"cron": "0 2 * * *"}})
	encoded, _ = json.Marshal(result)
	if strings.Contains(string(encoded), `"isError":true`) || !strings.Contains(string(encoded), `"occurrences"`) {
		t.Fatalf("MCP preview failed: %s", encoded)
	}
}
