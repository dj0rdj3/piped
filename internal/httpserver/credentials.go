package httpserver

import (
	"net/http"

	"piped/internal/credentials"
	"piped/internal/httpserver/response"
	"piped/internal/store"
)

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	return listResponse(w, r, actor, "credentials", s.auth.Store.CredentialsPage, nil)
}

func (s *Server) createCredential(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var body credentials.CreateInput
	// Larger than account requests to allow a bounded PEM key plus JSON escaping.
	if err := response.DecodeLimit(w, r, &body, 64*1024); err != nil {
		return err
	}
	defer func() {
		body.PrivateKey = ""
		body.Passphrase = ""
	}()
	c, err := s.config.Credentials.Create(r.Context(), actor, body)
	if err != nil {
		return response.CredentialFailure(err)
	}
	response.JSON(w, 201, map[string]any{"credential": c})
	return nil
}

func (s *Server) renameCredential(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	id := r.PathValue("id")
	var body struct {
		Name string `json:"name"`
	}
	if err := response.Decode(w, r, &body); err != nil {
		return err
	}
	if err := s.config.Credentials.Rename(r.Context(), actor, id, body.Name); err != nil {
		return response.CredentialFailure(err)
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	id := r.PathValue("id")
	var body struct{}
	if err := response.Decode(w, r, &body); err != nil {
		return err
	}
	if err := s.config.Credentials.Delete(r.Context(), actor, id); err != nil {
		return response.CredentialFailure(err)
	}
	w.WriteHeader(204)
	return nil
}
