package httpserver

import (
	"net/http"
	"strconv"

	"piped/internal/execution"
	"piped/internal/httpserver/response"
	"piped/internal/store"
)

func (s *Server) startRun(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input execution.StartRequest
	if err := response.Decode(w, r, &input); err != nil {
		return err
	}
	if input.JobID != "" && input.JobID != r.PathValue("id") {
		return response.Fail(400, "job_id does not match the request path")
	}
	input.JobID = r.PathValue("id")
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) > 1 || len(keys) == 1 && (keys[0] == "" || input.RequestID != "" && input.RequestID != keys[0]) {
		return response.Fail(400, "provide one matching request identity")
	}
	if len(keys) == 1 {
		input.RequestID = keys[0]
	}
	run, err := s.config.Executions.Start(r.Context(), actor, input)
	if err != nil {
		return response.ExecutionFailure(err)
	}
	response.JSON(w, 201, map[string]any{"run": run})
	return nil
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	runs, err := s.config.Executions.History(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		return response.ExecutionFailure(err)
	}
	response.JSON(w, 200, map[string]any{"runs": runs})
	return nil
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	view, err := s.config.Executions.View(r.Context(), actor, r.PathValue("run"))
	if err != nil {
		return response.ExecutionFailure(err)
	}
	response.JSON(w, 200, view)
	return nil
}

func (s *Server) runOutput(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	request := execution.OutputRequest{RunID: r.PathValue("run"), HostID: r.PathValue("host")}
	if raw := r.URL.Query().Get("tail"); raw != "" {
		if raw != "1" || r.URL.Query().Get("after") != "" {
			return response.Fail(400, "use tail=1 without an output cursor")
		}
		request.Tail = true
	}
	if raw := r.URL.Query().Get("after"); raw != "" {
		after, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return response.ExecutionFailure(execution.ErrOutputCursor)
		}
		request.After = after
	}
	page, err := s.config.Executions.Output(r.Context(), actor, request)
	if err != nil {
		return response.ExecutionFailure(err)
	}
	response.JSON(w, 200, page)
	return nil
}

func (s *Server) runInput(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input struct {
		Data string `json:"data"`
	}
	if err := response.Decode(w, r, &input); err != nil {
		return err
	}
	if err := s.config.Executions.Input(r.Context(), actor, execution.InputRequest{RunID: r.PathValue("run"), HostID: r.PathValue("host"), Data: input.Data}); err != nil {
		return response.ExecutionFailure(err)
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) cancelRunHost(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	if err := s.config.Executions.Cancel(r.Context(), actor, execution.HostRequest{RunID: r.PathValue("run"), HostID: r.PathValue("host")}); err != nil {
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
	if err := s.config.Executions.Approve(r.Context(), actor, execution.ApprovalRequest{RunID: r.PathValue("run"), HostID: r.PathValue("host"), Fingerprint: input.Fingerprint, Accept: *input.Accept}); err != nil {
		return response.ExecutionFailure(err)
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) readiness(w http.ResponseWriter, _ *http.Request) {
	if !s.config.Executions.Ready() {
		response.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) runtimeStatus(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	status, err := s.config.Executions.Runtime(r.Context(), actor)
	if err != nil {
		return response.ExecutionFailure(err)
	}
	response.JSON(w, http.StatusOK, status)
	return nil
}
