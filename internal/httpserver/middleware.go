package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"piped/internal/httpserver/development"
	"piped/internal/httpserver/response"
	"piped/internal/lifecycle"
	"piped/internal/store"
)

const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; " +
	"object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

func (s *Server) protect(next http.Handler) http.Handler {
	origin, _ := url.Parse(s.config.Origin) // Validated before constructing the server.
	// Some browsers do not include ws/wss in connect-src 'self'. Limit the
	// explicit WebSocket source to the configured public origin.
	socketOrigin := "ws" + strings.TrimPrefix(s.config.Origin, "http")
	policy := strings.Replace(contentSecurityPolicy, "connect-src 'self';", "connect-src 'self' "+socketOrigin+";", 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", policy)
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if s.secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		w.Header().Set("Cache-Control", "no-store")
		// Enforce the same host boundary for browser routes, assets and bearer
		// transports. Forwarded headers cannot authorize another request host.
		if !strings.EqualFold(r.Host, origin.Host) && !strings.EqualFold(r.Host, s.config.Listen) {
			response.JSON(w, http.StatusForbidden, map[string]string{"error": "untrusted request host"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/mcp" {
			// Socket handshakes share this budget. Upgraded terminals manage
			// their own lifetime and bound each storage/control operation.
			ctx, cancel := context.WithTimeout(r.Context(), lifecycle.RequestTimeout)
			defer cancel()
			r = r.WithContext(ctx)
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead {
			// The custom header is not CORS-safelisted. No CORS permission is granted.
			// Check Origin too, including requests from a different same-site origin.
			origin := r.Header.Get("Origin")
			keyAPI := strings.HasPrefix(r.URL.Path, "/api/v1/") || s.config.Development && development.KeyRequest(r.URL.Path)
			if (!keyAPI && r.Header.Get("X-Piped-Request") != "1") || (origin != "" && origin != s.config.Origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				response.JSON(w, 403, map[string]string{"error": "cross-origin request rejected"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) endpoint(fn response.Endpoint, authenticated bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var actor store.Actor
		if authenticated {
			cookie, err := r.Cookie(s.cookieName)
			if err != nil {
				response.JSON(w, 401, map[string]string{"error": "sign in required"})
				return
			}
			actor, err = s.auth.Resolve(r.Context(), cookie.Value)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrUnauthorized) {
					s.clearCookie(w)
					response.JSON(w, 401, map[string]string{"error": "session expired; sign in again"})
					return
				}
				response.WriteError(w, err, s.config.Logger)
				return
			}
		}
		if err := fn(w, r, actor); err != nil {
			response.WriteError(w, err, s.config.Logger)
		}
	}
}
