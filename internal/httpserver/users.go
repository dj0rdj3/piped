package httpserver

import (
	"net/http"
	"strconv"

	"piped/internal/httpserver/response"
	"piped/internal/store"
)

func (s *Server) users(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	return listResponse(w, r, actor, "users", s.auth.Store.UsersPage, nil)
}

func (s *Server) reauthenticate(w http.ResponseWriter, r *http.Request, actor store.Actor, password string) error {
	// Bearer-key routes authorize their explicit scope before reaching a handler
	// and again inside its write transaction. Browser routes only resolve cookies.
	if actor.APIKeyHash != "" {
		return nil
	}
	if err := s.throttle(w, r, actor.Username); err != nil {
		return err
	}
	return s.auth.Reauthenticate(actor, password)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var body struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		CurrentPassword string `json:"current_password"`
	}
	if err := response.Decode(w, r, &body); err != nil {
		return err
	}
	if err := s.reauthenticate(w, r, actor, body.CurrentPassword); err != nil {
		return err
	}
	u, err := s.auth.CreateUser(r.Context(), actor, body.Username, body.Password)
	if err != nil {
		return err
	}
	response.JSON(w, 201, map[string]any{"user": u})
	return nil
}

func userID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, response.Fail(400, "invalid user ID")
	}
	return id, nil
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	id, err := userID(r)
	if err != nil {
		return err
	}
	if id == actor.ID {
		return response.Fail(409, "you cannot remove your own account")
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
	}
	if err := response.Decode(w, r, &body); err != nil {
		return err
	}
	if err := s.reauthenticate(w, r, actor, body.CurrentPassword); err != nil {
		return err
	}
	if err := s.auth.Store.DeleteUser(r.Context(), actor, id); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	id, err := userID(r)
	if err != nil {
		return err
	}
	var body struct {
		Password        string `json:"password"`
		CurrentPassword string `json:"current_password"`
	}
	if err := response.Decode(w, r, &body); err != nil {
		return err
	}
	if err := s.reauthenticate(w, r, actor, body.CurrentPassword); err != nil {
		return err
	}
	if err := s.auth.ChangePassword(r.Context(), actor, id, body.Password); err != nil {
		return err
	}
	if id == actor.ID {
		s.clearCookie(w)
	}
	w.WriteHeader(204)
	return nil
}
