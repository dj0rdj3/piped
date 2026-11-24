package integration

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"piped/internal/store"
)

func TestCredentialLifecycle(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	input := map[string]string{"mode": "generate", "name": "Deploy key"}
	w := h.request("POST", "/api/credentials", input, cookie)
	expect(t, w, 201)
	var created struct {
		Credential store.Credential `json:"credential"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	c := created.Credential
	if c.Algorithm != "ssh-ed25519" || !strings.HasPrefix(c.PublicKey, "ssh-ed25519 ") || !strings.HasPrefix(c.Fingerprint, "SHA256:") {
		t.Fatalf("invalid metadata: %+v", c)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(c.PublicKey, " admin@"+hostname) {
		t.Fatal("generated public key omitted comment")
	}
	for _, method := range []string{"GET", "POST"} {
		expect(t, h.request(method, "/api/credentials", input, nil), 401)
	}
	w = h.request("GET", "/api/credentials", nil, cookie)
	expect(t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response can be cached")
	}
	if strings.Contains(w.Body.String(), "private_key") || strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), "encrypted_secret") {
		t.Fatal("credential response exposes secrets")
	}
	expect(t, h.request("POST", "/api/credentials", input, cookie), 409)
	path := "/api/credentials/" + c.ID
	expect(t, h.request("PATCH", path, map[string]string{"name": "Renamed"}, cookie), 204)
	var listed struct {
		Credentials []store.Credential `json:"credentials"`
	}
	w = h.request("GET", "/api/credentials", nil, cookie)
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Credentials) != 1 || listed.Credentials[0].Name != "Renamed" || listed.Credentials[0].PublicKey != c.PublicKey {
		t.Fatal("rename changed key or list failed")
	}
	// There is no secret viewing, export, or key-replacement endpoint.
	for _, suffix := range []string{"", "/private-key", "/download", "/export"} {
		expect(t, h.request("GET", path+suffix, nil, cookie), 404)
	}
	expect(t, h.request("DELETE", path, map[string]string{}, cookie), 204)
	expect(t, h.request("DELETE", path, map[string]string{}, cookie), 404)
	expect(t, h.request("POST", "/api/auth/logout", nil, cookie), 204)
	expect(t, h.request("GET", "/api/credentials", nil, cookie), 401)
	expect(t, h.request("POST", "/api/credentials", input, cookie), 401)
}

func TestCredentialAuthenticationAndCSRF(t *testing.T) {
	h := newHarness(t, true)
	cookie := h.login(t, "admin", testPassword)
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		path := "/api/credentials"
		if method != "POST" {
			path += "/" + strings.Repeat("a", 32)
		}
		expect(t, h.request(method, path, map[string]string{}, nil), 401)

		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		expect(t, w, 403)
		r = httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.AddCookie(cookie)
		r.Header.Set("X-Piped-Request", "1")
		r.Header.Set("Origin", "https://untrusted.example")
		w = httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		expect(t, w, 403)
	}
	expect(t, h.request("GET", "/api/credentials", nil, cookie), 200)
}

func TestProtectedKeyImportDoesNotExposeSecrets(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("ssh-secret-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	private := string(pem.EncodeToMemory(block))
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	originalPublic := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " original@import-host"
	input := map[string]string{"name": "Import", "mode": "import", "private_key": private, "public_key": originalPublic, "passphrase": "ssh-secret-passphrase"}
	w := h.request("POST", "/api/credentials", input, cookie)
	expect(t, w, 201)
	var result struct {
		Credential store.Credential `json:"credential"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Credential.PublicKey != originalPublic {
		t.Fatal("import replaced original public key comment")
	}
	listed := h.request("GET", "/api/credentials", nil, cookie)
	expect(t, listed, 200)
	if !strings.Contains(listed.Body.String(), " original@import-host") {
		t.Fatal("listing replaced original comment")
	}
	if !strings.Contains(w.Body.String(), `"has_passphrase":true`) {
		t.Fatal("missing passphrase metadata")
	}
	if strings.Contains(w.Body.String(), private) || strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), "ssh-secret-passphrase") || strings.Contains(w.Body.String(), testPassword) {
		t.Fatal("import response exposed secret")
	}
	input["public_key"] = "invalid public key"
	expect(t, h.request("POST", "/api/credentials", input, cookie), 400)
	input["passphrase"] = "wrong-secret-passphrase"
	w = h.request("POST", "/api/credentials", input, cookie)
	expect(t, w, 400)
	if strings.Contains(w.Body.String(), "wrong-secret-passphrase") || strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Fatal("import failure exposed secret")
	}
}

func TestCredentialRequestValidation(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	for _, raw := range []string{`{"unknown":"value"}`, `{} {}`, `{"private_key":"` + strings.Repeat("a", 66000) + `"}`, `{"mode":1}`, `[]`} {
		r := httptest.NewRequest("POST", "/api/credentials", strings.NewReader(raw))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Piped-Request", "1")
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		expect(t, w, 400)
	}
	for _, id := range []string{"bad", "-1", strings.Repeat("z", 32)} {
		expect(t, h.request("DELETE", "/api/credentials/"+id, map[string]string{}, cookie), 400)
	}
	for _, input := range []map[string]string{
		{"mode": "generate", "name": ""},
		{"mode": "password", "name": "Name"},
		{"mode": "import", "name": "Name", "private_key": "sensitive invalid key"},
	} {
		expect(t, h.request("POST", "/api/credentials", input, cookie), 400)
	}
}
