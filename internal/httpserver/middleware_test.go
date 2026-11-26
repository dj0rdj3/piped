package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPIDeadlineCoversAuthenticationAndCancelsFinishedRequests(t *testing.T) {
	for _, path := range []string{"/api/auth/login", "/api/v1/me", "/api/development/v1/me", "/mcp"} {
		t.Run(path, func(t *testing.T) {
			server := &Server{config: Config{Origin: "http://127.0.0.1:8080"}}
			var operation context.Context
			handler := server.protect(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				operation = request.Context()
				deadline, ok := operation.Deadline()
				if !ok || time.Until(deadline) > 20*time.Second {
					t.Error("API authentication/storage can wait indefinitely")
				}
			}))
			request := httptest.NewRequest(http.MethodGet, server.config.Origin+path, nil)
			handler.ServeHTTP(httptest.NewRecorder(), request)
			if operation == nil || operation.Err() != context.Canceled {
				t.Fatal("finished request retained its deadline timer or background work")
			}
			if request.Context().Err() != nil {
				t.Fatal("API middleware cancelled the caller's context")
			}
		})
	}
}

func TestOriginSchemeCasingKeepsCookieAndHeaderSecurity(t *testing.T) {
	for _, fixture := range []struct {
		configured, canonical, socket, cookie string
		secure                                bool
	}{
		{"HTTPS://piped.example", "https://piped.example", "wss://piped.example", "__Host-piped_session", true},
		{"HtTpS://piped.example", "https://piped.example", "wss://piped.example", "__Host-piped_session", true},
		{"HTTP://127.0.0.1:8080", "http://127.0.0.1:8080", "ws://127.0.0.1:8080", "piped_session", false},
	} {
		t.Run(fixture.configured, func(t *testing.T) {
			handler, err := New(nil, Config{Origin: fixture.configured, Listen: "127.0.0.1:8080"})
			if err != nil {
				t.Fatal(err)
			}
			for _, originHeader := range []string{"", fixture.canonical} {
				request := httptest.NewRequest(http.MethodPost, fixture.canonical+"/api/auth/logout", nil)
				request.Header.Set("X-Piped-Request", "1")
				request.Header.Set("Origin", originHeader)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusNoContent {
					t.Fatalf("canonical browser origin rejected: %d", response.Code)
				}
				cookies := response.Result().Cookies()
				if len(cookies) != 1 || cookies[0].Name != fixture.cookie || cookies[0].Secure != fixture.secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].MaxAge != -1 {
					t.Fatal("cookie security did not follow the parsed URL scheme")
				}
				if !strings.Contains(response.Header().Get("Content-Security-Policy"), "connect-src 'self' "+fixture.socket+";") {
					t.Fatal("CSP used the wrong WebSocket origin")
				}
				if (response.Header().Get("Strict-Transport-Security") != "") != fixture.secure {
					t.Fatal("HSTS did not follow the parsed URL scheme")
				}
			}
		})
	}
}
