package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"piped/internal/auth"
	"piped/internal/store"
)

func (h *harness) bearerRequest(method, path, token string, body any, configure func(*http.Request)) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	r := httptest.NewRequest(method, h.origin+path, bytes.NewReader(encoded))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if configure != nil {
		configure(r)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func (h *harness) createPersonalKey(t *testing.T, cookie *http.Cookie, scopes ...string) (store.APIKey, string) {
	t.Helper()
	w := h.request("POST", "/api/api-keys", map[string]any{"name": "Automation", "scopes": scopes, "expires_in_hours": 24, "current_password": testPassword}, cookie)
	expect(t, w, 201)
	var result struct {
		Key   store.APIKey `json:"key"`
		Token string       `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Token, auth.UserAPIKeyPrefix) {
		t.Fatal("wrong token namespace")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("issuance is cacheable")
	}
	return result.Key, result.Token
}

func TestProductionAPIKeyLifecycleAndOwnership(t *testing.T) {
	h := newHarness(t, true)
	cookie := h.login(t, "admin", testPassword)
	key, token := h.createPersonalKey(t, cookie, store.ScopeUsersRead, store.ScopeCredentialsRead)
	w := h.request("GET", "/api/api-keys", nil, cookie)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), key.ID) {
		t.Fatal("new key missing")
	}
	for _, secret := range []string{token, auth.TokenHash(token), "password_hash"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("listing leaked authentication material")
		}
	}
	expect(t, h.bearerRequest("GET", "/api/v1/users", token, nil, nil), 200)
	expect(t, h.bearerRequest("GET", "/api/v1/credentials", token, nil, nil), 200)
	expect(t, h.bearerRequest("GET", "/api/v1/jobs", token, nil, nil), 403)
	expect(t, h.bearerRequest("POST", "/api/v1/users", token, map[string]string{"username": "denied", "password": "long-password"}, nil), 403)
	expect(t, h.request("GET", "/api/v1/users", nil, cookie), 401)
	expect(t, h.bearerRequest("GET", "/api/api-keys", token, nil, nil), 401)
	expect(t, h.bearerRequest("GET", "/api/v1/api-keys", token, nil, nil), 404)
	// Another administrator cannot list or revoke this owner's keys.
	expect(t, h.request("POST", "/api/users", map[string]string{"username": "other", "password": testPassword, "current_password": testPassword}, cookie), 201)
	otherCookie := h.login(t, "other", testPassword)
	w = h.request("GET", "/api/api-keys", nil, otherCookie)
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), key.ID) {
		t.Fatal("cross-owner listing")
	}
	path := "/api/api-keys/" + key.ID
	expect(t, h.request("DELETE", path, map[string]string{"current_password": testPassword}, otherCookie), 404)
	expect(t, h.request("DELETE", path, map[string]string{"current_password": "wrong"}, cookie), 401)
	expect(t, h.bearerRequest("GET", "/api/v1/me", token, nil, nil), 200)
	expect(t, h.request("DELETE", path, map[string]string{"current_password": testPassword}, cookie), 204)
	expect(t, h.bearerRequest("GET", "/api/v1/me", token, nil, nil), 401)
	expect(t, h.request("DELETE", path, map[string]string{"current_password": testPassword}, cookie), 404)
}

func TestProductionAPIValidationAndAuthentication(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	for _, input := range []map[string]any{
		{"name": "Test", "scopes": []string{store.ScopeUsersRead}, "expires_in_hours": 24},
		{"name": "", "scopes": []string{store.ScopeUsersRead}, "expires_in_hours": 24, "current_password": testPassword},
		{"name": "Test", "scopes": []string{"*"}, "expires_in_hours": 24, "current_password": testPassword},
		{"name": "Test", "scopes": []string{store.ScopeUsersRead}, "expires_in_hours": -1, "current_password": testPassword},
		{"name": "Test", "scopes": []string{store.ScopeUsersRead}, "expires_in_hours": 2161, "current_password": testPassword},
	} {
		w := h.request("POST", "/api/api-keys", input, cookie)
		if w.Code != 400 && w.Code != 401 {
			t.Fatalf("invalid input accepted: %d", w.Code)
		}
	}
	_, token := h.createPersonalKey(t, cookie, store.ScopeUsersRead, store.ScopeUsersWrite)
	for _, configure := range []func(*http.Request){
		func(r *http.Request) { r.Host = "evil.example" },
		func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		expect(t, h.bearerRequest("GET", "/api/v1/users", token, nil, configure), 403)
	}
	expect(t, h.bearerRequest("GET", "/api/v1/users", token, nil, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+token) }), 401)
	expect(t, h.bearerRequest("GET", "/api/v1/users?api_key="+token, "", nil, nil), 401)
	// Mutations need no browser CSRF header but still require a scoped bearer.
	w := h.bearerRequest("POST", "/api/v1/users", token, map[string]string{"username": "api-user", "password": testPassword}, nil)
	expect(t, w, 201)
	expect(t, h.bearerRequest("POST", "/api/v1/users/1/password", token, map[string]string{"password": "new-owner-password"}, nil), 204)
	expect(t, h.bearerRequest("GET", "/api/v1/users", token, nil, nil), 401)
	expect(t, h.request("GET", "/api/api-keys", nil, cookie), 401)
}

func TestPersonalKeyExpiryAndInternalKeyIsolation(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	owner, err := h.store.UserByName(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	// Existing internal keys remain hidden and unusable without development enablement.
	internal, internalToken, err := auth.NewAPIKey(owner, "Internal", []string{store.ScopeUsersRead}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.CreateLocalAPIKey(context.Background(), owner, internal, auth.TokenHash(internalToken)); err != nil {
		t.Fatal(err)
	}
	expect(t, h.bearerRequest("GET", "/api/v1/users", internalToken, nil, nil), 401)
	w := h.request("GET", "/api/api-keys", nil, cookie)
	if strings.Contains(w.Body.String(), internal.ID) {
		t.Fatal("internal key visible")
	}
	expect(t, h.request("DELETE", "/api/api-keys/"+internal.ID, map[string]string{"current_password": testPassword}, cookie), 404)
	expired, token, err := auth.NewUserAPIKey(owner, "Expired", []string{store.ScopeUsersRead}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	expired.CreatedAt = time.Now().Unix() - 7200
	expired.ExpiresAt = time.Now().Unix()
	actor, err := h.auth.Resolve(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.CreateUserAPIKey(context.Background(), actor, expired, auth.TokenHash(token)); err != nil {
		t.Fatal(err)
	}
	expect(t, h.bearerRequest("GET", "/api/v1/users", token, nil, nil), 401)
}

func TestProductionAPIKeyRateLimit(t *testing.T) {
	h := newHarness(t, false)
	for index := range 121 {
		w := h.bearerRequest("GET", "/api/v1/me", "", nil, nil)
		want := 401
		if index == 120 {
			want = 429
		}
		expect(t, w, want)
	}
}

func TestProductionAPIResourceLifecycleAndExecutionScope(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	_, token := h.createPersonalKey(t, cookie,
		store.ScopeCredentialsRead, store.ScopeCredentialsWrite,
		store.ScopeHostsRead, store.ScopeHostsWrite,
		store.ScopeJobsRead, store.ScopeJobsWrite)
	w := h.bearerRequest("POST", "/api/v1/credentials", token, map[string]string{"mode": "generate", "name": "API credential"}, nil)
	expect(t, w, 201)
	var credential struct {
		Credential store.Credential `json:"credential"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &credential); err != nil {
		t.Fatal(err)
	}
	w = h.bearerRequest("POST", "/api/v1/hosts", token, map[string]any{
		"name": "API host", "address": "127.0.0.1", "port": 22, "username": "deploy", "credential_id": credential.Credential.ID,
	}, nil)
	expect(t, w, 201)
	var host struct {
		Host store.Host `json:"host"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &host); err != nil {
		t.Fatal(err)
	}
	w = h.bearerRequest("POST", "/api/v1/jobs", token, map[string]any{
		"name": "API job", "script": "true", "host_ids": []string{host.Host.ID}, "execution_mode": "sequential",
		"secrets": []map[string]string{{"name": "TOKEN", "value": "fixture-secret-value"}},
	}, nil)
	expect(t, w, 201)
	var job struct {
		Job store.Job `json:"job"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	w = h.bearerRequest("GET", "/api/v1/jobs", token, nil, nil)
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), "fixture-secret-value") {
		t.Fatal("job API exposed a secret")
	}
	expect(t, h.bearerRequest("GET", "/api/v1/hosts", token, nil, nil), 200)
	expect(t, h.bearerRequest("POST", "/api/v1/jobs/"+job.Job.ID+"/run", token, map[string]int64{"version": job.Job.Version}, nil), 403)
	expect(t, h.bearerRequest("GET", "/api/v1/jobs/"+job.Job.ID+"/runs", token, nil, nil), 403)
	expect(t, h.bearerRequest("DELETE", "/api/v1/jobs/"+job.Job.ID, token, map[string]int64{"version": job.Job.Version}, nil), 204)
	expect(t, h.bearerRequest("DELETE", "/api/v1/hosts/"+host.Host.ID, token, map[string]int64{"version": host.Host.Version}, nil), 204)
	expect(t, h.bearerRequest("DELETE", "/api/v1/credentials/"+credential.Credential.ID, token, nil, nil), 204)
}

func TestPersonalAPIKeyNoExpirationRequiresExplicitSelection(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	for _, body := range []map[string]any{
		{"name": "Missing expiry", "scopes": []string{store.ScopeUsersRead}, "current_password": testPassword},
		{"name": "Null expiry", "scopes": []string{store.ScopeUsersRead}, "expires_in_hours": nil, "current_password": testPassword},
	} {
		expect(t, h.request("POST", "/api/api-keys", body, cookie), 400)
	}
	w := h.request("POST", "/api/api-keys", map[string]any{"name": "No expiration", "scopes": []string{store.ScopeUsersRead}, "expires_in_hours": 0, "current_password": testPassword}, cookie)
	expect(t, w, 201)
	var issued struct {
		Key   store.APIKey `json:"key"`
		Token string       `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Key.ExpiresAt != 0 {
		t.Fatal("no-expiration key has an expiry timestamp")
	}
	if err := h.store.PurgeAPIKeys(context.Background(), time.Now().AddDate(100, 0, 0)); err != nil {
		t.Fatal(err)
	}
	expect(t, h.bearerRequest("GET", "/api/v1/users", issued.Token, nil, nil), 200)
	expect(t, h.request("DELETE", "/api/api-keys/"+issued.Key.ID, map[string]string{"current_password": testPassword}, cookie), 204)
	expect(t, h.bearerRequest("GET", "/api/v1/users", issued.Token, nil, nil), 401)
}
