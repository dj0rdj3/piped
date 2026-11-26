package keyauth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"piped/internal/store"
)

func TestStorageTimeoutDoesNotInvalidateBearerKey(t *testing.T) {
	handler := New(Config{
		Origin: "http://127.0.0.1:8080", Listen: "127.0.0.1:8080",
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		ClientIP: func(*http.Request) string { return "127.0.0.1" },
		Allow:    func(string, time.Time) bool { return true },
		Resolve: func(context.Context, string) (store.Actor, error) {
			return store.Actor{}, context.DeadlineExceeded
		},
	}, func(http.ResponseWriter, *http.Request, store.Actor) {
		t.Fatal("timed out authentication reached a privileged handler")
	})
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/me", nil)
	request.Header.Set("Authorization", "Bearer existing-valid-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") == "" {
		t.Fatal("transient storage timeout did not return a retryable failure", response.Code)
	}
	if response.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("database timeout falsely reported an invalid token")
	}
}
