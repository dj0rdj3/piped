// Package httpserver exposes the application API and embedded frontend.
package httpserver

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"piped/internal/auth"
	"piped/internal/credentials"
	"piped/internal/execution"
	"piped/internal/hosts"
	"piped/internal/httpserver/development"
	"piped/internal/httpserver/keyauth"
	"piped/internal/httpserver/response"
	"piped/internal/httpserver/terminal"
	"piped/internal/jobs"
	"piped/internal/notifications"
	"piped/internal/store"
)

const DevelopmentBuild = development.Enabled

type Config struct {
	Notifications  *notifications.Service
	Origin         string
	Listen         string
	Assets         fs.FS
	Logger         *slog.Logger
	Credentials    *credentials.Service
	Jobs           *jobs.Service
	Hosts          *hosts.Service
	Executions     *execution.Engine
	Development    bool
	TrustedProxies []netip.Prefix
}

type Server struct {
	auth       *auth.Service
	config     Config
	secure     bool
	cookieName string
	limiter    *limiter
	apiLimiter *limiter
}

func ValidateConfig(c Config) error {
	for _, prefix := range c.TrustedProxies {
		if !prefix.IsValid() {
			return errors.New("trusted proxies must be valid IP prefixes")
		}
	}
	if c.Development && !DevelopmentBuild {
		return errors.New("development integrations are unavailable in production builds")
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("public URL must be an http(s) origin without a path, query, or credentials")
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	originIP := net.ParseIP(u.Hostname())
	loopback := ip != nil && ip.IsLoopback() &&
		(u.Hostname() == "localhost" || originIP != nil && originIP.IsLoopback())
	if c.Development && !loopback {
		return errors.New("development integrations require a loopback listener and public URL")
	}
	if u.Scheme == "http" && !loopback {
		return errors.New("HTTP is allowed only on loopback; use an HTTPS public URL behind a TLS proxy for network access")
	}
	return nil
}

func New(a *auth.Service, c Config) (http.Handler, error) {
	if err := ValidateConfig(c); err != nil {
		return nil, err
	}
	origin, _ := url.Parse(c.Origin)
	c.Origin = origin.String()
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	s := &Server{
		auth:       a,
		config:     c,
		secure:     origin.Scheme == "https",
		cookieName: "piped_session",
		limiter:    newRateLimiter(10, 15*time.Minute),
		apiLimiter: newRateLimiter(120, time.Minute),
	}
	if s.secure {
		s.cookieName = "__Host-piped_session"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { response.JSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/ready", s.readiness)
	mux.HandleFunc("POST /api/auth/login", s.endpoint(s.login, false))
	mux.HandleFunc("POST /api/auth/logout", s.endpoint(s.logout, false))
	mux.HandleFunc("GET /api/auth/me", s.endpoint(s.me, true))
	mux.HandleFunc("GET /api/api-keys", s.endpoint(s.listAPIKeys, true))
	mux.HandleFunc("POST /api/api-keys", s.endpoint(s.createAPIKey, true))
	mux.HandleFunc("DELETE /api/api-keys/{id}", s.endpoint(s.revokeAPIKey, true))
	routes := s.resourceRoutes()
	for _, route := range routes {
		mux.HandleFunc(route.Method+" /api"+route.Path, s.endpoint(route.Handler, true))
	}
	apiRoutes := append(append([]response.Route{}, routes...), response.Route{Method: "GET", Path: "/me", Handler: s.me})
	for _, route := range apiRoutes {
		mux.Handle(route.Method+" /api/v1"+route.Path, keyauth.New(keyauth.Config{
			Origin: c.Origin, Listen: c.Listen, Logger: c.Logger, Allow: s.apiLimiter.allow, ClientIP: s.clientIP,
			Resolve: a.ResolveUserAPIKey,
		}, func(w http.ResponseWriter, r *http.Request, actor store.Actor) {
			if route.Scope != "" && !store.HasScope(actor.Scopes, route.Scope) {
				response.WriteError(w, store.ErrForbidden, c.Logger)
				return
			}
			if err := route.Handler(w, r, actor); err != nil {
				response.WriteError(w, err, c.Logger)
			}
		}))
	}
	if c.Executions != nil {
		// Terminal sockets accept browser sessions only, never bearer keys.
		mux.HandleFunc("GET /api/runs/{run}/hosts/{host}/terminal", s.endpoint(terminal.New(a.Store, c.Executions, c.Origin, s.apiLimiter.allow).Serve, true))
	}
	if c.Development {
		development.Register(mux, development.Config{
			Auth: a, Origin: c.Origin, Listen: c.Listen, Logger: c.Logger,
			Credentials: c.Credentials, Hosts: c.Hosts, Jobs: c.Jobs, Executions: c.Executions, Notifications: c.Notifications,
			Allow: s.apiLimiter.allow, ClientIP: s.clientIP,
		}, routes, s.me)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, 404, map[string]string{"error": "API endpoint not found"})
	})
	mux.HandleFunc("/", s.assets)
	return s.protect(mux), nil
}
