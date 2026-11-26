package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"piped/internal/auth"
	"piped/internal/httpserver/response"
	"piped/internal/store"
)

func (s *Server) throttle(w http.ResponseWriter, r *http.Request, account string) error {
	ip := s.clientIP(r)
	now := time.Now()
	if !s.limiter.allow("ip:"+ip, now) || !s.limiter.allow("account:"+auth.TokenHash(account), now) {
		w.Header().Set("Retry-After", "900")
		return response.Fail(429, "too many authentication attempts; try again in 15 minutes")
	}
	return nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, _ store.Actor) error {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := response.Decode(w, r, &body); err != nil {
		return err
	}
	if err := s.throttle(w, r, strings.ToLower(strings.TrimSpace(body.Username))); err != nil {
		return err
	}
	u, token, err := s.auth.Login(r.Context(), body.Username, body.Password)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(auth.SessionLifetime.Seconds()),
		Expires:  time.Now().Add(auth.SessionLifetime),
	})
	response.JSON(w, 200, map[string]any{"user": u, "development": s.config.Development})
	return nil
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ store.Actor) error {
	if cookie, err := r.Cookie(s.cookieName); err == nil {
		if err := s.auth.Store.DeleteSession(r.Context(), auth.TokenHash(cookie.Value)); err != nil {
			return err
		}
	}
	s.clearCookie(w)
	w.WriteHeader(204)
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	response.JSON(w, 200, map[string]any{"user": actor.User, "development": s.config.Development})
	return nil
}

// Cleanup runs on the server lifecycle context; sessions also expire on every lookup.
func Cleanup(ctx context.Context, s *store.Store, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.PurgeSessions(cleanup, time.Now().Unix())
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Error("session cleanup failed", "error", err)
		}
		cleanup, cancel = context.WithTimeout(ctx, 5*time.Second)
		err = s.PurgeAPIKeys(cleanup, time.Now())
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Error("API key cleanup failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
