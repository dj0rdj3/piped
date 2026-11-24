package httpserver

import (
	"net/http"
	"strconv"

	"piped/internal/httpserver/response"
	"piped/internal/store"
)

func (s *Server) startRun(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input struct {
		Version int64 `json:"version"`
	}
	if err := response.Decode(w, r, &input); err != nil {
		return err
	}
	run, err := s.config.Executions.Start(r.Context(), actor, r.PathValue("id"), input.Version)
	if err != nil {
		return response.ExecutionFailure(err)
	}
	response.JSON(w, 201, map[string]any{"run": run})
	return nil
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	runs, err := s.auth.Store.Runs(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		return response.ExecutionFailure(err)
	}
	response.JSON(w, 200, map[string]any{"runs": runs})
	return nil
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	run, err := s.auth.Store.Run(r.Context(), actor, r.PathValue("run"))
	if err != nil {
		return response.ExecutionFailure(err)
	}
	keys, err := s.config.Executions.Pending(r.Context(), actor, run.ID)
	if err != nil {
		return response.ExecutionFailure(err)
	}
	response.JSON(w, 200, map[string]any{"run": run, "host_keys": keys})
	return nil
}

func (s *Server) runOutput(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	if raw := r.URL.Query().Get("tail"); raw != "" {
		if raw != "1" || r.URL.Query().Get("after") != "" {
			return response.Fail(400, "use tail=1 without an output cursor")
		}
		chunks, truncated, err := s.auth.Store.RunOutputTail(r.Context(), actor, r.PathValue("run"), r.PathValue("host"))
		if err != nil {
			return response.ExecutionFailure(err)
		}
		response.JSON(w, 200, map[string]any{"chunks": chunks, "truncated": truncated})
		return nil
	}
	var after int64
	if raw := r.URL.Query().Get("after"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			return response.Fail(400, "invalid output cursor")
		}
		after = value
	}
	chunks, err := s.auth.Store.RunOutput(r.Context(), actor, r.PathValue("run"), r.PathValue("host"), after)
	if err != nil {
		return response.ExecutionFailure(err)
	}
	response.JSON(w, 200, map[string]any{"chunks": chunks})
	return nil
}

func (s *Server) runInput(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input struct {
		Data string `json:"data"`
	}
	if err := response.Decode(w, r, &input); err != nil {
		return err
	}
	if err := s.config.Executions.Input(r.Context(), actor, r.PathValue("run"), r.PathValue("host"), input.Data); err != nil {
		return response.ExecutionFailure(err)
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) cancelRunHost(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	if err := s.config.Executions.Cancel(r.Context(), actor, r.PathValue("run"), r.PathValue("host")); err != nil {
		return response.ExecutionFailure(err)
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) approveRunHost(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input struct {
		Fingerprint string `json:"fingerprint"`
		Accept      *bool  `json:"accept"`
	}
	if err := response.Decode(w, r, &input); err != nil {
		return err
	}
	if input.Accept == nil {
		return response.Fail(400, "choose Accept or Deny")
	}
	if err := s.config.Executions.Approve(r.Context(), actor, r.PathValue("run"), r.PathValue("host"), input.Fingerprint, *input.Accept); err != nil {
		return response.ExecutionFailure(err)
	}
	w.WriteHeader(204)
	return nil
}
