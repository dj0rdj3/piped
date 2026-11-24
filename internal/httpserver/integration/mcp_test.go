//go:build development

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"piped/internal/store"
)

type handlerTransport struct {
	handler http.Handler
	token   string
}

func (transport handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+transport.token)
	response := httptest.NewRecorder()
	transport.handler.ServeHTTP(response, request)
	return response.Result(), nil
}

func TestMCPOfficialClientCompatibility(t *testing.T) {
	h := newHarness(t, false, true)
	_, token := h.issueKey(t, store.ScopeUsersRead, store.ScopeCredentialsRead)
	client := mcp.NewClient(&mcp.Implementation{Name: "piped-test", Version: "1.0.0"}, nil)
	ctx := context.Background()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   h.origin + "/mcp",
		HTTPClient: &http.Client{Transport: handlerTransport{handler: h.handler, token: token}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("SDK tools/list: %v, %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_users", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("SDK tools/call: %v, %v", result, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(encoded), "admin") || strings.Contains(string(encoded), token) {
		t.Fatal("invalid SDK tool result")
	}
}

func (h *harness) mcpRequest(t *testing.T, token, method string, params any) map[string]any {
	t.Helper()
	w := h.bearerRequest("POST", "/mcp", token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, func(r *http.Request) {
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	})
	expect(t, w, 200)
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("invalid MCP JSON: %v: %s", err, w.Body.String())
	}
	return response
}

func TestMCPReadOnlyToolsAndAuthentication(t *testing.T) {
	h := newHarness(t, false, true)
	key, token := h.issueKey(t, store.ScopeUsersRead, store.ScopeCredentialsRead)
	response := h.mcpRequest(t, token, "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "test", "version": "1"}})
	if response["error"] != nil {
		t.Fatalf("initialize failed: %v", response)
	}
	response = h.mcpRequest(t, token, "tools/list", map[string]any{})
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing tools result: %v", response)
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("unexpected tool visibility: %v", result)
	}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if tool["name"] != "list_users" && tool["name"] != "list_credentials" {
			t.Fatal("write tool exposed to read key")
		}
	}
	response = h.mcpRequest(t, token, "tools/call", map[string]any{"name": "list_users", "arguments": map[string]any{}})
	encoded, _ := json.Marshal(response)
	if !strings.Contains(string(encoded), "admin") || response["error"] != nil {
		t.Fatalf("list failed: %s", encoded)
	}
	for _, forbidden := range []string{"password_hash", "argon2", token, "APIKeyHash", "SessionHash"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("MCP returned authentication material")
		}
	}
	response = h.mcpRequest(t, token, "tools/call", map[string]any{"name": "create_user", "arguments": map[string]string{"username": "forbidden", "password": "test-password"}})
	if response["error"] == nil {
		t.Fatal("unscoped tool call accepted")
	}
	if err := h.store.RevokeLocalAPIKey(context.Background(), key.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, h.bearerRequest("POST", "/mcp", token, map[string]any{}, nil), 401)
}

func TestMCPCredentialWritesNeverReturnPrivateMaterial(t *testing.T) {
	h := newHarness(t, false, true)
	_, token := h.issueKey(t, store.ScopeCredentialsRead, store.ScopeCredentialsWrite)
	response := h.mcpRequest(t, token, "tools/call", map[string]any{"name": "create_credential", "arguments": map[string]string{"mode": "generate", "name": "MCP key"}})
	encoded, _ := json.Marshal(response)
	if response["error"] != nil || !strings.Contains(string(encoded), "ssh-ed25519") {
		t.Fatalf("generation failed: %s", encoded)
	}
	for _, forbidden := range []string{"PRIVATE KEY", "private_key", "encrypted_secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("MCP exposed private material: %s", forbidden)
		}
	}
	response = h.mcpRequest(t, token, "tools/call", map[string]any{"name": "create_credential", "arguments": map[string]string{"mode": "import", "name": "Invalid", "private_key": "sensitive-invalid-private-material", "passphrase": "sensitive-passphrase"}})
	encoded, _ = json.Marshal(response)
	if !strings.Contains(string(encoded), "isError") {
		t.Fatalf("invalid import succeeded: %s", encoded)
	}
	if strings.Contains(string(encoded), "sensitive-") {
		t.Fatal("MCP import error echoed secret input")
	}
	// Schema errors must not echo malformed password or key values either.
	response = h.mcpRequest(t, token, "tools/call", map[string]any{"name": "create_credential", "arguments": map[string]any{"mode": "import", "name": "Invalid", "private_key": []string{"sensitive-schema-secret"}}})
	encoded, _ = json.Marshal(response)
	if strings.Contains(string(encoded), "sensitive-schema-secret") {
		t.Fatal("MCP schema error echoed secret input")
	}
	user, err := h.store.UserByName(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), user.PasswordHash) {
		t.Fatal("MCP error exposed stored hash")
	}
}
