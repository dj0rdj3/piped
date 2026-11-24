package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticationRateLimitBehindTrustedProxy(t *testing.T) {
	h := newHarness(t, false)
	for index := range 12 {
		r := httptest.NewRequest(http.MethodPost, h.origin+"/api/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"missing%d","password":"wrong"}`, index)))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Piped-Request", "1")
		client := "203.0.113.5"
		if index == 11 {
			client = "203.0.113.6"
		}
		// Rotating the attacker-controlled prefix must not bypass the actual
		// client's bucket. A different client through the same proxy stays usable.
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d, %s, 127.0.0.2", index, client))
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		want := http.StatusUnauthorized
		if index == 10 {
			want = http.StatusTooManyRequests
		}
		expect(t, w, want)
	}
}
