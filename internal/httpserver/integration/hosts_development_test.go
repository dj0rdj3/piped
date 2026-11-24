//go:build development

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"piped/internal/store"
)

func TestHostDevelopmentAPIAndMCP(t *testing.T) {
	h := newHarness(t, false, true)
	_, token := h.issueKey(t, store.ScopeHostsRead, store.ScopeHostsWrite)
	_, readToken := h.issueKey(t, store.ScopeHostsRead)
	input := hostBody()
	expect(t, h.bearerRequest("POST", "/api/v1/hosts", readToken, input, nil), 403)
	w := h.bearerRequest("POST", "/api/v1/hosts", token, input, nil)
	expect(t, w, 201)
	assertNoHostSecrets(t, w.Body.String())
	host := responseHost(t, w)
	tools := h.mcpRequest(t, token, "tools/list", map[string]any{})
	encoded, _ := json.Marshal(tools)
	for _, name := range []string{"list_hosts", "save_host", "delete_host"} {
		if !strings.Contains(string(encoded), name) {
			t.Fatalf("host MCP tool missing: %s", name)
		}
	}
	readTools := h.mcpRequest(t, readToken, "tools/list", map[string]any{})
	encoded, _ = json.Marshal(readTools)
	if strings.Contains(string(encoded), "save_host") || strings.Contains(string(encoded), "delete_host") || !strings.Contains(string(encoded), "list_hosts") {
		t.Fatal("MCP scope filtering failed")
	}
	result := h.mcpRequest(t, token, "tools/call", map[string]any{"name": "list_hosts", "arguments": map[string]any{}})
	encoded, _ = json.Marshal(result)
	assertNoHostSecrets(t, string(encoded))
	if !strings.Contains(string(encoded), host.ID) {
		t.Fatal("MCP list missing host")
	}
	input["id"], input["version"], input["name"] = host.ID, host.Version, "MCP updated"
	result = h.mcpRequest(t, token, "tools/call", map[string]any{"name": "save_host", "arguments": input})
	encoded, _ = json.Marshal(result)
	assertNoHostSecrets(t, string(encoded))
	if strings.Contains(string(encoded), `"isError":true`) || !strings.Contains(string(encoded), "MCP updated") {
		t.Fatalf("MCP update failed: %s", encoded)
	}
	result = h.mcpRequest(t, token, "tools/call", map[string]any{"name": "save_host", "arguments": map[string]any{"password": []string{"sensitive-schema-value"}}})
	encoded, _ = json.Marshal(result)
	if strings.Contains(string(encoded), "sensitive-schema-value") {
		t.Fatal("MCP schema validation leaked secret")
	}
	result = h.mcpRequest(t, token, "tools/call", map[string]any{"name": "delete_host", "arguments": map[string]any{"id": host.ID, "version": host.Version + 1}})
	encoded, _ = json.Marshal(result)
	if !strings.Contains(string(encoded), `"removed":true`) {
		t.Fatalf("MCP delete failed: %s", encoded)
	}
	listed := h.bearerRequest("GET", "/api/v1/hosts", readToken, nil, nil)
	expect(t, listed, 200)
	if listed.Body.String() != "{\"hosts\":[]}\n" {
		t.Fatal("deleted host still listed")
	}
	key, revokedToken := h.issueKey(t, store.ScopeHostsWrite)
	if err := h.store.RevokeLocalAPIKey(context.Background(), key.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, h.bearerRequest("POST", "/api/v1/hosts", revokedToken, hostBody(), nil), 401)
}
