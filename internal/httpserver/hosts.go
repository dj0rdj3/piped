package httpserver

import (
	"net/http"

	"piped/internal/hosts"
	"piped/internal/httpserver/response"
	"piped/internal/store"
)

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	return listResponse(w, r, actor, "hosts", s.auth.Store.HostsPage, nil)
}

func (s *Server) saveHost(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input *hosts.Input
	if err := response.Decode(w, r, &input); err != nil {
		return err
	}
	if input == nil {
		return response.Fail(400, "provide a host object")
	}
	defer func() {
		input.Password = nil
		input.SudoPassword = nil
	}()
	host, err := s.config.Hosts.Save(r.Context(), actor, r.PathValue("id"), *input)
	if err != nil {
		return response.HostFailure(err)
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	response.JSON(w, status, map[string]any{"host": host})
	return nil
}

func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input struct {
		Version int64 `json:"version"`
	}
	if err := response.Decode(w, r, &input); err != nil {
		return err
	}
	if err := s.config.Hosts.Delete(r.Context(), actor, r.PathValue("id"), input.Version); err != nil {
		return response.HostFailure(err)
	}
	w.WriteHeader(204)
	return nil
}
