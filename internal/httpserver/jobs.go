package httpserver

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"piped/internal/httpserver/response"
	"piped/internal/jobs"
	"piped/internal/store"
)

func (s *Server) schedulePreview(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	result, err := s.config.Jobs.PreviewSchedule(r.Context(), actor, r.URL.Query().Get("cron"))
	if err != nil {
		return response.JobFailure(err)
	}
	response.JSON(w, 200, result)
	return nil
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	items, err := s.config.Jobs.List(r.Context(), actor)
	if err != nil {
		return response.JobFailure(err)
	}
	var hosts []store.Host
	if s.config.Hosts != nil && r.URL.Query().Has("limit") && r.URL.Query().Get("q") != "" {
		hosts, err = s.config.Hosts.List(r.Context(), actor)
		if err != nil && !errors.Is(err, store.ErrForbidden) {
			return response.HostFailure(err)
		}
	}
	return listResponse(w, r, "jobs", items, func(item store.Job) listEntry {
		text := append([]string{item.Name}, item.HostTags...)
		for _, host := range hosts {
			if slices.Contains(item.ExcludedHostIDs, host.ID) {
				continue
			}
			included := slices.Contains(item.HostIDs, host.ID)
			for _, tag := range host.Tags {
				if slices.Contains(item.HostTags, tag) {
					included = true
					break
				}
			}
			if included {
				text = append(text, host.Name)
			}
		}
		return listEntry{id: item.ID, name: item.Name, search: strings.Join(text, " ")}
	}, map[string]any{"server_timezone": jobs.ServerTimezone()})
}

func (s *Server) saveJob(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input *jobs.Input
	// Bound JSON escaping overhead for the script, secret values, and host IDs.
	if err := response.DecodeLimit(w, r, &input, 1024*1024); err != nil {
		return err
	}
	if input == nil {
		return response.Fail(400, "provide a job object")
	}
	defer func() {
		for index := range input.Secrets {
			input.Secrets[index].Value = nil
		}
	}()
	job, err := s.config.Jobs.Save(r.Context(), actor, r.PathValue("id"), *input)
	if err != nil {
		return response.JobFailure(err)
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	response.JSON(w, status, map[string]any{"job": job})
	return nil
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input struct {
		Version int64 `json:"version"`
	}
	if err := response.Decode(w, r, &input); err != nil {
		return err
	}
	if err := s.config.Jobs.Delete(r.Context(), actor, r.PathValue("id"), input.Version); err != nil {
		return response.JobFailure(err)
	}
	w.WriteHeader(204)
	return nil
}
