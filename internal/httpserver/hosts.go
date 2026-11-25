package httpserver

import (
	"net/http"
	"strings"

	"piped/internal/hosts"
	"piped/internal/httpserver/response"
	"piped/internal/store"
)

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	items, err := s.config.Hosts.List(r.Context(), actor)
	if err != nil {
		return response.HostFailure(err)
	}
	return listResponse(w, r, "hosts", items, func(item store.Host) listEntry {
		return listEntry{id: item.ID, name: item.Name, search: strings.Join(append([]string{item.Name, item.Address, item.Username, item.CredentialName}, item.Tags...), " "), tags: item.Tags}
	}, nil)
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
