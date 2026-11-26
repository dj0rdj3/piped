package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"piped/internal/auth"
	"piped/internal/credentials"
	"piped/internal/execution"
	"piped/internal/hosts"
	"piped/internal/httpserver"
	"piped/internal/jobs"
	"piped/internal/notifications"
	"piped/internal/secrets"
	"piped/internal/store"
	"piped/internal/testdatabase"
)

const testPassword = "correct horse battery staple"

type harness struct {
	handler  http.Handler
	store    *store.Store
	auth     *auth.Service
	origin   string
	requests int
}

func newHarness(t *testing.T, secure bool, development ...bool) *harness {
	t.Helper()
	driver, dsn := testdatabase.Settings(t)
	return newHarnessWithDatabase(t, secure, store.Config{Driver: driver, DSN: dsn, Path: filepath.Join(t.TempDir(), "data", "piped.db")}, development...)
}

func newHarnessWithDatabase(t *testing.T, secure bool, configuration store.Config, development ...bool) *harness {
	t.Helper()
	s, err := store.OpenDatabase(context.Background(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a, err := auth.New(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.InitAdmin(context.Background(), "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	origin := "http://127.0.0.1:8080"
	if secure {
		origin = "https://piped.example"
	}
	vault, err := secrets.New(bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	credentialService, err := credentials.New(context.Background(), s, vault)
	if err != nil {
		t.Fatal(err)
	}
	hostService, err := hosts.New(context.Background(), s, vault)
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.New(context.Background(), s, vault)
	if err != nil {
		t.Fatal(err)
	}
	notificationService, err := notifications.New(context.Background(), s, vault)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := execution.New(context.Background(), s, vault, slog.New(slog.NewTextHandler(io.Discard, nil)), 7)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	h, err := httpserver.New(a, httpserver.Config{
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
		Listen:         "127.0.0.1:8080",
		Origin:         origin,
		Assets: fstest.MapFS{
			"index.html":    {Data: []byte("piped frontend")},
			"assets/app.js": {Data: []byte("console.log('piped')")},
		},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Credentials:   credentialService,
		Hosts:         hostService,
		Jobs:          jobService,
		Notifications: notificationService,
		Executions:    engine,
		Development:   len(development) > 0 && development[0],
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{handler: h, store: s, auth: a, origin: origin}
}

func (h *harness) request(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	var input io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		input = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, h.origin+path, input)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Piped-Request", "1")
	r.Header.Set("Origin", h.origin)
	h.requests++
	r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", h.requests)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func expect(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status %d, want %d: %s", response.Code, status, response.Body.String())
	}
}

func (h *harness) login(t *testing.T, username, password string) *http.Cookie {
	t.Helper()
	w := h.request("POST", "/api/auth/login", map[string]string{"username": username, "password": password}, nil)
	expect(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected session cookie: %v", cookies)
	}
	return cookies[0]
}

func TestLoginSessionLifecycle(t *testing.T) {
	h := newHarness(t, false)
	expect(t, h.request("GET", "/api/auth/me", nil, nil), 401)
	for _, name := range []string{"admin", "missing", "' OR 1=1--"} {
		w := h.request("POST", "/api/auth/login", map[string]string{"username": name, "password": "wrong"}, nil)
		expect(t, w, 401)
		if w.Body.String() != "{\"error\":\"invalid username or password\"}\n" {
			t.Fatal("login leaks account existence")
		}
	}
	cookie := h.login(t, " ADMIN ", testPassword)
	if !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge != int(auth.SessionLifetime.Seconds()) {
		t.Fatalf("cookie attributes: %+v", cookie)
	}
	if len(cookie.Value) != 43 {
		t.Fatal("unexpected token length")
	}
	w := h.request("GET", "/api/auth/me", nil, cookie)
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "argon2") || strings.Contains(w.Body.String(), cookie.Value) {
		t.Fatal("session response exposes secrets")
	}
	expect(t, h.request("POST", "/api/auth/logout", nil, cookie), 204)
	expect(t, h.request("GET", "/api/auth/me", nil, cookie), 401)
	expect(t, h.request("POST", "/api/auth/logout", nil, cookie), 204)
}

func TestSecureCookieAndExpiration(t *testing.T) {
	h := newHarness(t, true)
	cookie := h.login(t, "admin", testPassword)
	if cookie.Name != "__Host-piped_session" || !cookie.Secure || cookie.Domain != "" {
		t.Fatalf("secure cookie: %+v", cookie)
	}
	u, err := h.store.UserByName(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("x", 43)
	if err := h.store.CreateSession(context.Background(), u, auth.TokenHash(token), time.Now().Unix()-10, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	cookie.Value = token
	expect(t, h.request("GET", "/api/auth/me", nil, cookie), 401)
}

func TestUserManagement(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	create := map[string]string{"username": "member", "password": "member12", "current_password": testPassword}
	wrong := map[string]string{"username": "member", "password": create["password"], "current_password": "wrong"}
	expect(t, h.request("POST", "/api/users", wrong, cookie), 401)
	w := h.request("POST", "/api/users", create, cookie)
	expect(t, w, 201)
	var result struct {
		User store.User `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.User.PasswordHash != "" {
		t.Fatal("password hash returned")
	}
	expect(t, h.request("POST", "/api/users", create, cookie), 409)
	memberCookie := h.login(t, "member", create["password"])
	expect(t, h.request("GET", "/api/users", nil, memberCookie), 200)
	passwordPath := fmt.Sprintf("/api/users/%d/password", result.User.ID)
	expect(t, h.request("POST", passwordPath, map[string]string{"password": "updated1", "current_password": testPassword}, cookie), 204)
	expect(t, h.request("GET", "/api/auth/me", nil, memberCookie), 401)
	expect(t, h.request("POST", "/api/auth/login", map[string]string{"username": "member", "password": create["password"]}, nil), 401)
	memberCookie = h.login(t, "member", "updated1")
	expect(t, h.request("DELETE", fmt.Sprintf("/api/users/%d", result.User.ID), map[string]string{"current_password": testPassword}, cookie), 204)
	expect(t, h.request("GET", "/api/auth/me", nil, memberCookie), 401)
	expect(t, h.request("DELETE", "/api/users/1", map[string]string{"current_password": testPassword}, cookie), 409)
	expect(t, h.request("POST", "/api/users/1/password", map[string]string{"password": "a fresh administrator password", "current_password": testPassword}, cookie), 204)
	expect(t, h.request("GET", "/api/auth/me", nil, cookie), 401)
	h.login(t, "admin", "a fresh administrator password")
}

func TestUnauthenticatedAccessDenied(t *testing.T) {
	h := newHarness(t, false)
	for _, tc := range []struct{ method, path string }{{"GET", "/api/users"}, {"POST", "/api/users"}, {"POST", "/api/users/1/password"}, {"DELETE", "/api/users/1"}} {
		expect(t, h.request(tc.method, tc.path, map[string]string{}, nil), 401)
	}
	expect(t, h.request("GET", "/api/does-not-exist", nil, nil), 404)
	expect(t, h.request("POST", "/api/auth/signup", map[string]string{}, nil), 404)
}

func TestCSRFAndSecurityHeaders(t *testing.T) {
	h := newHarness(t, true)
	for _, headers := range []map[string]string{
		{}, {"X-Piped-Request": "1", "Origin": "https://evil.example"},
		{"X-Piped-Request": "1", "Origin": "null"},
		{"X-Piped-Request": "1", "Origin": h.origin, "Sec-Fetch-Site": "cross-site"},
	} {
		r := httptest.NewRequest("POST", "/api/auth/logout", nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		expect(t, w, 403)
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("unexpected CORS permission")
		}
	}
	w := h.request("GET", "/", nil, nil)
	expect(t, w, 200)
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Strict-Transport-Security", "Cache-Control", "Referrer-Policy"} {
		if w.Header().Get(header) == "" {
			t.Errorf("missing %s", header)
		}
	}
	expect(t, h.request("GET", "/assets/", nil, nil), 404)
	expect(t, h.request("GET", "/missing-file", nil, nil), 404)
	w = h.request("GET", "/assets/app.js", nil, nil)
	expect(t, w, 200)
	if !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("hashed assets not cached")
	}
}

func TestMalformedRequests(t *testing.T) {
	h := newHarness(t, false)
	for _, tc := range []struct {
		contentType, body string
		status            int
	}{
		{"text/plain", `{}`, 415},
		{"application/json", `{`, 400},
		{"application/json", `{} {}`, 400},
		{"application/json", `{"username":"admin","password":"secret","extra":true}`, 400},
		{"application/json", `{"username":"` + strings.Repeat("a", 17000) + `"}`, 400},
	} {
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.contentType)
		r.Header.Set("X-Piped-Request", "1")
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		expect(t, w, tc.status)
	}
	cookie := h.login(t, "admin", testPassword)
	for _, id := range []string{"0", "-1", "bad", "999999999999999999999999"} {
		expect(t, h.request("DELETE", "/api/users/"+id, map[string]string{}, cookie), 400)
	}
	for _, password := range []string{strings.Repeat("x", 7), strings.Repeat("x", 129)} {
		expect(t, h.request("POST", "/api/users", map[string]string{"username": "member", "password": password, "current_password": testPassword}, cookie), 400)
	}
}

func TestAuthenticationRateLimitIgnoresUntrustedForwardedHeaders(t *testing.T) {
	h := newHarness(t, false)
	for i := 0; i < 11; i++ {
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"missing%d","password":"wrong"}`, i)))
		r.RemoteAddr = "192.0.2.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Piped-Request", "1")
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i))
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		want := 401
		if i == 10 {
			want = 429
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("missing retry guidance")
			}
		}
		expect(t, w, want)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		origin, listen string
		valid          bool
	}{
		{"http://127.0.0.1:8080", "127.0.0.1:8080", true},
		{"http://localhost:5173", "127.0.0.1:8080", true},
		{"https://piped.example", "0.0.0.0:8080", true},
		{"http://piped.example", "127.0.0.1:8080", false},
		{"http://127.0.0.1:8080", "0.0.0.0:8080", false},
		{"https://piped.example/", "127.0.0.1:8080", false},
		{"https://user:pass@piped.example", "127.0.0.1:8080", false},
		{"https://piped.example?x=1", "127.0.0.1:8080", false},
		{"ftp://piped.example", "127.0.0.1:8080", false},
	} {
		err := httpserver.ValidateConfig(httpserver.Config{Origin: tc.origin, Listen: tc.listen})
		if (err == nil) != tc.valid {
			t.Errorf("origin %q listen %q: %v", tc.origin, tc.listen, err)
		}
	}
}
