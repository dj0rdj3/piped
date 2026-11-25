//go:build development

package development

import (
	"context"
	"net/http"
	"strings"

	"piped/internal/httpserver/keyauth"
	"piped/internal/httpserver/response"
	"piped/internal/store"
)

type developmentActorKey struct{}

type server struct{ config Config }

const Enabled = true

func Register(mux *http.ServeMux, config Config, routes []response.Route, me response.Endpoint) {
	s := &server{config: config}
	routes = append(routes, response.Route{Method: "GET", Path: "/me", Handler: me})
	if s.config.Executions != nil {
		routes = append(routes, response.Route{Method: "DELETE", Path: "/runs/{run}", Scope: store.ScopeRunsWrite, Handler: s.deleteCompletedRun})
	}
	for _, route := range routes {
		mux.Handle(route.Method+" /api/development/v1"+route.Path, s.developmentAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := r.Context().Value(developmentActorKey{}).(store.Actor)
			if route.Scope != "" && !store.HasScope(actor.Scopes, route.Scope) {
				response.WriteError(w, store.ErrForbidden, config.Logger)
				return
			}
			if err := route.Handler(w, r, actor); err != nil {
				response.WriteError(w, err, config.Logger)
			}
		})))
	}
	mux.Handle("/mcp", s.developmentAuth(s.mcpHandler()))
}

func (s *server) developmentAuth(next http.Handler) http.Handler {
	return keyauth.New(keyauth.Config{
		Origin: s.config.Origin, Listen: s.config.Listen, Logger: s.config.Logger,
		Allow: s.config.Allow, ClientIP: s.config.ClientIP, Resolve: s.config.Auth.ResolveAPIKey,
	}, func(w http.ResponseWriter, r *http.Request, actor store.Actor) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), developmentActorKey{}, actor)))
	})
}

func KeyRequest(path string) bool { return strings.HasPrefix(path, "/api/development/v1/") }
