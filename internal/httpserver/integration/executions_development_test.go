//go:build development

package integration

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"piped/internal/store"
)

func TestArchivedOutputThroughDevelopmentAPIAndMCP(t *testing.T) {
	h := newHarness(t, false, true)
	cookie := h.login(t, "admin", testPassword)
	hostResponse := h.request("POST", "/api/hosts", hostBody(), cookie)
	expect(t, hostResponse, 201)
	host := responseHost(t, hostResponse)
	jobResponse := h.request("POST", "/api/jobs", jobBody(host.ID), cookie)
	expect(t, jobResponse, 201)
	job := responseJob(t, jobResponse)
	ctx := context.Background()
	actor, err := h.auth.Resolve(ctx, cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	runAdmission, err := h.store.AdmitRun(ctx, actor, store.RunAdmission{ID: "archive-tools", RequestID: "archive-tools", JobID: job.ID, Version: job.Version})
	run := runAdmission.PublicRun()
	if err != nil {
		t.Fatal(err)
	}
	for range 80 {
		if err := h.store.AppendOutput(ctx, run.ID, host.ID, []byte("\xe2\x82\xac\r\n")); err != nil {
			t.Fatal(err)
		}
	}
	want, _, err := h.store.RunOutputTail(ctx, actor, run.ID, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetRunHost(ctx, run.ID, host.ID, "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := h.store.CompressRunOutput(ctx, run.ID, host.ID); err != nil {
		t.Fatal(err)
	}
	_, token := h.issueKey(t, store.ScopeRunsRead)
	discovery := h.mcpRequest(t, token, "tools/list", map[string]any{})
	encoded, _ := json.Marshal(discovery)
	if !strings.Contains(string(encoded), `"read_run_output"`) {
		t.Fatal("output tool missing from discovery")
	}
	path := "/api/development/v1/runs/" + run.ID + "/hosts/" + host.ID + "/output?tail=1"
	response := h.bearerRequest("GET", path, token, nil, nil)
	expect(t, response, 200)
	var result struct {
		Chunks []store.OutputChunk `json:"chunks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || !reflect.DeepEqual(result.Chunks, want) {
		t.Fatalf("development API changed archived output: %v", err)
	}
	reply := h.mcpRequest(t, token, "tools/call", map[string]any{"name": "read_run_output", "arguments": map[string]any{"run_id": run.ID, "host_id": host.ID, "tail": true}})
	encoded, err = json.Marshal(reply["result"])
	if err != nil {
		t.Fatal(err)
	}
	var toolResult struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(encoded, &toolResult); err != nil || toolResult.IsError || len(toolResult.Content) != 1 {
		t.Fatalf("MCP read failed: %v", err)
	}
	if err := json.Unmarshal([]byte(toolResult.Content[0].Text), &result); err != nil || !reflect.DeepEqual(result.Chunks, want) {
		t.Fatalf("MCP changed archived output: %v", err)
	}
}

func TestExecutionDevelopmentScopesAndToolDiscovery(t *testing.T) {
	h := newHarness(t, false, true)
	_, authorToken := h.issueKey(t, store.ScopeJobsRead, store.ScopeJobsWrite)
	encoded, _ := json.Marshal(h.mcpRequest(t, authorToken, "tools/list", map[string]any{}))
	if strings.Contains(string(encoded), `"run_job"`) {
		t.Fatal("definition-author key received execution capability")
	}
	expect(t, h.bearerRequest("POST", "/api/development/v1/jobs/missing/run", authorToken, map[string]any{"version": 1, "request_id": "fixture-intent"}, nil), 403)
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
	expect(t, h.bearerRequest("POST", "/api/development/v1/runs/missing/hosts/missing/input", readToken, map[string]string{"data": "danger"}, nil), 403)
	_, writeToken := h.issueKey(t, store.ScopeRunsWrite)
	encoded, _ = json.Marshal(h.mcpRequest(t, writeToken, "tools/list", map[string]any{}))
	for _, name := range []string{"run_job", "cancel_run_host", "send_run_input", "approve_run_host_key"} {
		if !strings.Contains(string(encoded), name) {
			t.Fatalf("missing write tool %s", name)
		}
	}
	expect(t, h.bearerRequest("GET", "/api/development/v1/runs/missing", writeToken, nil, nil), 403)
	expect(t, h.bearerRequest("POST", "/api/development/v1/jobs/missing/run", writeToken, map[string]any{"version": 1, "request_id": "fixture-intent"}, nil), 404)
	expect(t, h.bearerRequest("GET", "/api/development/v1/runs/missing", readToken, nil, nil), 404)
	expect(t, h.bearerRequest("DELETE", "/api/development/v1/runs/missing", readToken, nil, nil), 403)
}

func TestMCPAndHTTPShareAdmissionAndOutputCommands(t *testing.T) {
	h := newHarness(t, false, true)
	cookie := h.login(t, "admin", testPassword)
	input := hostBody()
	input["address"], input["port"] = "127.0.0.1", 1
	host := responseHost(t, h.request("POST", "/api/hosts", input, cookie))
	job := responseJob(t, h.request("POST", "/api/jobs", jobBody(host.ID), cookie))
	_, token := h.issueKey(t, store.ScopeRunsRead, store.ScopeRunsWrite)
	// Discover the schema as a real client does before using request identities.
	tools, _ := json.Marshal(h.mcpRequest(t, token, "tools/list", map[string]any{}))
	if !strings.Contains(string(tools), `"request_id"`) {
		t.Fatal("admission identity missing from MCP schema")
	}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		reply := h.mcpRequest(t, token, "tools/call", map[string]any{"name": name, "arguments": args})
		result, ok := reply["result"].(map[string]any)
		if !ok {
			t.Fatalf("missing tool result: %v", reply)
		}
		return result
	}
	args := map[string]any{"job_id": job.ID, "version": job.Version, "request_id": "shared-mcp-admission"}
	for range 2 {
		result := call("run_job", args)
		if failed, _ := result["isError"].(bool); failed {
			t.Fatalf("admission: %v", result)
		}
	}
	response := h.bearerRequest("POST", "/api/development/v1/jobs/"+job.ID+"/run", token, args, nil)
	expect(t, response, 201)
	if !containsRunCount(t, h.request("GET", "/api/jobs/"+job.ID+"/runs", nil, cookie), 1) {
		t.Fatal("transport switch replayed execution")
	}
	var started struct {
		Run store.Run `json:"run"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []map[string]any{
		{"run_id": started.Run.ID, "host_id": host.ID, "after": -1},
		{"run_id": started.Run.ID, "host_id": host.ID, "after": 1, "tail": true},
	} {
		result := call("read_run_output", invalid)
		if failed, _ := result["isError"].(bool); !failed {
			t.Fatalf("invalid cursor accepted: %v", result)
		}
	}
	expect(t, h.bearerRequest("GET", "/api/development/v1/runs/"+started.Run.ID+"/hosts/"+host.ID+"/output?after=-1", token, nil, nil), 400)
	args["version"] = job.Version + 1
	if failed, _ := call("run_job", args)["isError"].(bool); !failed {
		t.Fatal("MCP reused identity for a different fingerprint")
	}
}

func TestMalformedExecutionIDsAreClientErrorsAcrossKeyAPIsAndMCP(t *testing.T) {
	h := newHarness(t, false, true)
	cookie := h.login(t, "admin", testPassword)
	_, production := h.createPersonalKey(t, cookie, store.ScopeRunsRead, store.ScopeRunsWrite)
	_, development := h.issueKey(t, store.ScopeRunsRead, store.ScopeRunsWrite, store.ScopeCredentialsWrite)
	for _, api := range []struct{ prefix, token string }{{"/api/v1", production}, {"/api/development/v1", development}} {
		for _, path := range []string{"/jobs/bad%00id/runs", "/runs/bad%00id", "/runs/valid/hosts/bad%00id/output"} {
			expect(t, h.bearerRequest("GET", api.prefix+path, api.token, nil, nil), 400)
		}
		expect(t, h.bearerRequest("POST", api.prefix+"/jobs/bad%00id/run", api.token, map[string]any{"version": 1, "request_id": "malformed-id"}, nil), 400)
	}
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"list_runs", map[string]any{"job_id": "bad\x00id"}},
		{"get_run", map[string]any{"run_id": "bad\x00id"}},
		{"read_run_output", map[string]any{"run_id": "valid", "host_id": "bad\x00id"}},
		{"run_job", map[string]any{"job_id": "bad\x00id", "version": 1, "request_id": "malformed-id"}},
		{"rename_credential", map[string]any{"id": "bad\x00id", "name": "Valid name"}},
		{"delete_credential", map[string]any{"id": "bad\x00id"}},
	} {
		response := h.mcpRequest(t, development, "tools/call", map[string]any{"name": call.name, "arguments": call.args})
		encoded, err := json.Marshal(response["result"])
		if err != nil || !strings.Contains(string(encoded), `"isError":true`) || (!strings.Contains(string(encoded), "not found") && !strings.Contains(string(encoded), "invalid credential ID") && !strings.Contains(string(encoded), "invalid execution request")) {
			t.Fatalf("malformed identifier was not a sanitized client error for %s: %s", call.name, encoded)
		}
	}
}
