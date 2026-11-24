//go:build development

package development

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"piped/internal/httpserver/response"
	"piped/internal/store"
)

type developmentActorKey struct{}

type server struct{ config Config }

const Enabled = true

func Register(mux *http.ServeMux, config Config, routes []response.Route, me response.Endpoint) {
	s := &server{config: config}
	routes = append(routes, response.Route{Method: "GET", Path: "/me", Scope: "", Handler: me})
	if s.config.Executions != nil {
		// Fixture cleanup is deliberately absent from browser routes.
		routes = append(routes, response.Route{Method: "DELETE", Path: "/runs/{run}", Scope: store.ScopeRunsWrite, Handler: s.deleteCompletedRun})
	}
	for _, route := range routes {
		mux.Handle(route.Method+" /api/v1"+route.Path, s.developmentAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := r.Context().Value(developmentActorKey{}).(store.Actor)
			if route.Scope != "" && !store.HasScope(actor.Scopes, route.Scope) {
				response.WriteError(w, store.ErrForbidden, s.config.Logger)
				return
			}
			if err := route.Handler(w, r, actor); err != nil {
				response.WriteError(w, err, s.config.Logger)
			}
		})))
	}
	mux.Handle("/mcp", s.developmentAuth(s.mcpHandler()))
}

func (s *server) developmentAuth(next http.Handler) http.Handler {
	origin, _ := url.Parse(s.config.Origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validHost := strings.EqualFold(r.Host, origin.Host) || strings.EqualFold(r.Host, s.config.Listen)
		if !validHost || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.config.Origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			response.JSON(w, 403, map[string]string{"error": "untrusted request origin or host"})
			return
		}
		ip := s.config.ClientIP(r)
		if !s.config.Allow("ip:"+ip, time.Now()) {
			w.Header().Set("Retry-After", "60")
			response.JSON(w, 429, map[string]string{"error": "too many development API requests"})
			return
		}
		values := r.Header.Values("Authorization")
		var scheme, token string
		if len(values) == 1 {
			scheme, token, _ = strings.Cut(values[0], " ")
		}
		if !strings.EqualFold(scheme, "Bearer") || token == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="piped-development"`)
			response.JSON(w, 401, map[string]string{"error": "a development API key is required"})
			return
		}
		actor, err := s.config.Auth.ResolveAPIKey(r.Context(), token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="piped-development", error="invalid_token"`)
			if errors.Is(err, store.ErrUnauthorized) {
				response.JSON(w, 401, map[string]string{"error": "API key invalid, expired, or revoked"})
			} else {
				response.WriteError(w, err, s.config.Logger)
			}
			return
		}
		if !s.config.Allow("key:"+actor.APIKeyHash, time.Now()) {
			w.Header().Set("Retry-After", "60")
			response.JSON(w, 429, map[string]string{"error": "too many development API requests"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		r = r.WithContext(context.WithValue(r.Context(), developmentActorKey{}, actor))
		next.ServeHTTP(w, r)
	})
}
