package httpserver

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"piped/internal/auth"
	"piped/internal/httpserver/response"
	"piped/internal/store"
)

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	keys, err := s.auth.Store.UserAPIKeys(r.Context(), actor)
	if err != nil {
		return err
	}
	return listResponse(w, r, "keys", keys, func(item store.APIKey) listEntry {
		return listEntry{id: item.ID, name: fmt.Sprintf("%020d", int64(math.MaxInt64)-item.CreatedAt), search: item.Name}
	}, map[string]any{"scopes": auth.APIKeyScopes()})
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var body struct {
		Name            string   `json:"name"`
		Scopes          []string `json:"scopes"`
		ExpiresInHours  *int     `json:"expires_in_hours"`
		CurrentPassword string   `json:"current_password"`
	}
	if err := response.Decode(w, r, &body); err != nil {
		return err
	}
	if body.ExpiresInHours == nil || *body.ExpiresInHours < 0 || *body.ExpiresInHours > 90*24 {
		return auth.ErrAPIKeyInput
	}
	if err := s.reauthenticate(w, r, actor, body.CurrentPassword); err != nil {
		return err
	}
	key, token, err := s.auth.CreateUserAPIKey(r.Context(), actor, body.Name, body.Scopes, time.Duration(*body.ExpiresInHours)*time.Hour)
	if err != nil {
		return err
	}
	response.JSON(w, 201, map[string]any{"key": key, "token": token})
	return nil
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var body struct {
		CurrentPassword string `json:"current_password"`
	}
	if err := response.Decode(w, r, &body); err != nil {
		return err
	}
	if err := s.reauthenticate(w, r, actor, body.CurrentPassword); err != nil {
		return err
	}
	err := s.auth.Store.RevokeUserAPIKey(r.Context(), actor, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return response.Fail(404, "API key not found")
	}
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
