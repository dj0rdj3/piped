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
	run, _, _, err := h.store.CreateRun(ctx, actor, "archive-tools", job.ID, job.Version)
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
	expect(t, h.bearerRequest("POST", "/api/development/v1/jobs/missing/run", authorToken, map[string]int{"version": 1}, nil), 403)
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
	expect(t, h.bearerRequest("POST", "/api/development/v1/jobs/missing/run", writeToken, map[string]int{"version": 1}, nil), 404)
	expect(t, h.bearerRequest("GET", "/api/development/v1/runs/missing", readToken, nil, nil), 404)
	expect(t, h.bearerRequest("DELETE", "/api/development/v1/runs/missing", readToken, nil, nil), 403)
}
