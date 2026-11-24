package integration

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"piped/internal/store"
)

func hostBody() map[string]any {
	return map[string]any{"name": "Home server", "address": "host.example.com", "port": 2222,
		"username": "deploy", "password": "ssh-sensitive-password", "run_as_sudo": true,
		"sudo_password": "sudo-sensitive-password", "tags": []string{"home", "linux"}}
}
func responseHost(t *testing.T, w *httptest.ResponseRecorder) store.Host {
	t.Helper()
	var response struct {
		Host store.Host `json:"host"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Host
}
func assertNoHostSecrets(t *testing.T, data string) {
	t.Helper()
	for _, secret := range []string{"ssh-sensitive-password", "sudo-sensitive-password", testPassword, "encrypted_password", "encrypted_sudo_password", `"password":`, `"sudo_password":`} {
		if strings.Contains(data, secret) {
			t.Fatalf("response exposed %s", secret)
		}
	}
}
func TestHostHTTPCRUDAndSharedAccess(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	input := hostBody()
	w := h.request("POST", "/api/hosts", input, cookie)
	expect(t, w, 201)
	assertNoHostSecrets(t, w.Body.String())
	host := responseHost(t, w)
	path := "/api/hosts/" + host.ID
	if !host.HasPassword || !host.HasSudoPassword || host.Port != 2222 {
		t.Fatal("missing host metadata")
	}
	expect(t, h.request("POST", "/api/hosts", input, cookie), 409)
	// Another administrator sees and can edit the same host.
	expect(t, h.request("POST", "/api/users", map[string]string{"username": "other", "password": "other-password", "current_password": testPassword}, cookie), 201)
	other := h.login(t, "other", "other-password")
	listed := h.request("GET", "/api/hosts", nil, other)
	expect(t, listed, 200)
	assertNoHostSecrets(t, listed.Body.String())
	if !strings.Contains(listed.Body.String(), host.ID) || listed.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("shared list or cache protection failed")
	}
	input["name"], input["version"] = "Renamed", host.Version
	delete(input, "password")
	delete(input, "sudo_password")
	w = h.request("PUT", path, input, other)
	expect(t, w, 200)
	updated := responseHost(t, w)
	if !updated.HasPassword || !updated.HasSudoPassword || updated.Name != "Renamed" || updated.Version != host.Version+1 {
		t.Fatal("edit lost secrets or version")
	}
	expect(t, h.request("PUT", path, input, other), 409)
	expect(t, h.request("DELETE", path, map[string]any{"version": host.Version}, cookie), 409)
	expect(t, h.request("DELETE", path, map[string]any{"version": updated.Version}, cookie), 204)
	expect(t, h.request("DELETE", path, map[string]any{"version": updated.Version}, cookie), 404)
	// No password readback or arbitrary execution routes exist.
	for _, suffix := range []string{"", "/password", "/sudo-password", "/secrets", "/exec"} {
		expect(t, h.request("GET", path+suffix, nil, cookie), 404)
	}
}

func TestHostHTTPAuthenticationAndValidation(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	path := "/api/hosts/" + strings.Repeat("a", 32)
	expect(t, h.request("GET", "/api/hosts", nil, nil), 401)
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		target := path
		if method == "POST" {
			target = "/api/hosts"
		}
		expect(t, h.request(method, target, hostBody(), nil), 401)
		r := httptest.NewRequest(method, target, strings.NewReader(`{}`))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		expect(t, w, 403)
	}
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"name":true}`, `{"unknown":"secret"}`, `{"password":"` + strings.Repeat("s", 17000) + `"}`} {
		r := httptest.NewRequest("POST", "/api/hosts", strings.NewReader(raw))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Piped-Request", "1")
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		expect(t, w, 400)
	}
	body := hostBody()
	body["address"] = "https://invalid.example"
	expect(t, h.request("POST", "/api/hosts", body, cookie), 400)
}
