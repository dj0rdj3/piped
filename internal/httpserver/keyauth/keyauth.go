// Package keyauth authenticates scoped bearer requests independently of browser sessions.
package keyauth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"piped/internal/httpserver/response"
	"piped/internal/store"
)

type Config struct {
	Origin, Listen string
	Logger         *slog.Logger
	Allow          func(string, time.Time) bool
	ClientIP       func(*http.Request) string
	Resolve        func(context.Context, string) (store.Actor, error)
}

func New(config Config, next func(http.ResponseWriter, *http.Request, store.Actor)) http.Handler {
	origin, _ := url.Parse(config.Origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validHost := strings.EqualFold(r.Host, origin.Host) || strings.EqualFold(r.Host, config.Listen)
		if !validHost || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != config.Origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			response.JSON(w, 403, map[string]string{"error": "untrusted request origin or host"})
			return
		}
		ip := config.ClientIP(r)
		if !config.Allow("ip:"+ip, time.Now()) {
			w.Header().Set("Retry-After", "60")
			response.JSON(w, 429, map[string]string{"error": "too many API requests"})
			return
		}
		values := r.Header.Values("Authorization")
		var scheme, token string
		if len(values) == 1 {
			scheme, token, _ = strings.Cut(values[0], " ")
		}
		if !strings.EqualFold(scheme, "Bearer") || token == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="piped"`)
			response.JSON(w, 401, map[string]string{"error": "an API key is required"})
			return
		}
		actor, err := config.Resolve(r.Context(), token)
		if err != nil {
			if errors.Is(err, store.ErrUnauthorized) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="piped", error="invalid_token"`)
				response.JSON(w, 401, map[string]string{"error": "API key invalid, expired, or revoked"})
			} else {
				response.WriteError(w, err, config.Logger)
			}
			return
		}
		if !config.Allow("key:"+actor.APIKeyHash, time.Now()) {
			w.Header().Set("Retry-After", "60")
			response.JSON(w, 429, map[string]string{"error": "too many API requests"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		next(w, r, actor)
	})
}
