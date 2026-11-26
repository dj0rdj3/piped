package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"piped/internal/store"
	"piped/internal/testdatabase"
)

func TestStalledPostgresAuthenticationTimesOutAndPreservesSession(t *testing.T) {
	driver, dsn := testdatabase.Settings(t)
	if driver != "postgres" {
		t.Skip("PostgreSQL network replies need request-context deadlines")
	}
	proxyDSN, stall, resume, blocked := testdatabase.StallingProxy(t, driver, dsn, "FROM users u JOIN sessions s ON s.user_id = u.id WHERE s.token_hash")
	defer resume()
	h := newHarnessWithDatabase(t, false, store.Config{Driver: driver, DSN: proxyDSN})
	cookie := h.login(t, "admin", testPassword)
	stall()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, h.origin+"/api/auth/me", nil).WithContext(ctx)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.handler.ServeHTTP(response, request)
		close(done)
	}()
	// Always join the request before the harness closes its database.
	defer func() { cancel(); resume(); <-done }()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("authentication did not reach the stalled database reply")
	}
	select {
	case <-done:
	case <-time.After(17 * time.Second):
		t.Fatal("connected PostgreSQL stall pinned API authentication indefinitely")
	}
	expect(t, response, http.StatusServiceUnavailable)
	if response.Header().Get("Retry-After") == "" || len(response.Result().Cookies()) != 0 {
		t.Fatal("storage outage did not remain retryable or cleared a valid browser session")
	}
	resume()
	expect(t, h.request(http.MethodGet, "/api/auth/me", nil, cookie), http.StatusOK)
}
