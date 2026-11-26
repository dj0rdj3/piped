package integration

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"piped/internal/store"
)

func jobBody(hostID string) map[string]any {
	return map[string]any{"name": "Host health", "script": "#!/bin/sh\nprintf '%s\\n' \"$(hostname)\"\n", "host_ids": []string{hostID}, "execution_mode": "sequential", "stop_on_failure": true, "run_as_sudo": true}
}
func responseJob(t *testing.T, w *httptest.ResponseRecorder) store.Job {
	t.Helper()
	var response struct {
		Job store.Job `json:"job"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Job
}
func TestJobHTTPCRUDAndSharedAccess(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	target := h.request("POST", "/api/hosts", hostBody(), cookie)
	expect(t, target, 201)
	host := responseHost(t, target)
	input := jobBody(host.ID)
	w := h.request("POST", "/api/jobs", input, cookie)
	expect(t, w, 201)
	job := responseJob(t, w)
	if !job.RunAsSudo || job.Version != 1 || job.Script != input["script"] || len(job.HostIDs) != 1 || job.HostIDs[0] != host.ID {
		t.Fatalf("incomplete job: %+v", job)
	}
	expect(t, h.request("POST", "/api/jobs", input, cookie), 409)
	expect(t, h.request("POST", "/api/users", map[string]string{"username": "other", "password": "other-password", "current_password": testPassword}, cookie), 201)
	other := h.login(t, "other", "other-password")
	w = h.request("GET", "/api/jobs", nil, other)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), job.ID) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("shared list or cache protection failed")
	}
	if strings.Contains(w.Body.String(), testPassword) {
		t.Fatal("password returned")
	}
	input["name"], input["version"] = "Updated", job.Version
	input["execution_mode"], input["stop_on_failure"], input["run_as_sudo"] = "parallel", false, false
	path := "/api/jobs/" + job.ID
	w = h.request("PUT", path, input, other)
	expect(t, w, 200)
	updated := responseJob(t, w)
	if updated.RunAsSudo || updated.Version != 2 || updated.ExecutionMode != "parallel" || updated.StopOnFailure {
		t.Fatalf("update failed: %+v", updated)
	}
	expect(t, h.request("PUT", path, input, other), 409)
	expect(t, h.request("DELETE", path, map[string]any{"version": 1}, cookie), 409)
	expect(t, h.request("DELETE", path, map[string]any{"version": 2}, cookie), 204)
	expect(t, h.request("DELETE", path, map[string]any{"version": 2}, cookie), 404)
	expect(t, h.request("POST", path+"/run", map[string]any{"version": 2, "request_id": "fixture-intent"}, cookie), 404)
	expect(t, h.request("GET", "/api/jobs", nil, other), 200)
}
func TestJobHTTPAuthenticationAndValidation(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	path := "/api/jobs/" + strings.Repeat("a", 32)
	expect(t, h.request("GET", "/api/jobs", nil, nil), 401)
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		target := path
		if method == "POST" {
			target = "/api/jobs"
		}
		body := jobBody(strings.Repeat("b", 32))
		expect(t, h.request(method, target, body, nil), 401)
		r := httptest.NewRequest(method, h.origin+target, strings.NewReader(`{}`))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		expect(t, w, 403)
	}
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"script":true}`, `{"unknown":"value"}`, `{"script":"` + strings.Repeat("s", 1024*1024) + `"}`} {
		r := httptest.NewRequest("POST", h.origin+"/api/jobs", strings.NewReader(raw))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Piped-Request", "1")
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		expect(t, w, 400)
	}
	body := jobBody(strings.Repeat("b", 32))
	expect(t, h.request("POST", "/api/jobs", body, cookie), 400)
	body["host_ids"] = []string{}
	expect(t, h.request("POST", "/api/jobs", body, cookie), 400)
}

func TestJobSecretHTTPWriteOnlyUpdates(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	hostResponse := h.request("POST", "/api/hosts", hostBody(), cookie)
	expect(t, hostResponse, 201)
	input := jobBody(responseHost(t, hostResponse).ID)
	secretValue := "http-sensitive-job-value"
	input["secrets"] = []map[string]any{{"name": "API_TOKEN", "value": secretValue}}
	response := h.request("POST", "/api/jobs", input, cookie)
	expect(t, response, 201)
	job := responseJob(t, response)
	if len(job.SecretNames) != 1 || job.SecretNames[0] != "API_TOKEN" || strings.Contains(response.Body.String(), secretValue) {
		t.Fatal("secret metadata or readback incorrect")
	}
	listed := h.request("GET", "/api/jobs", nil, cookie)
	expect(t, listed, 200)
	if strings.Contains(listed.Body.String(), secretValue) || strings.Contains(listed.Body.String(), "encrypted_value") {
		t.Fatal("list exposed secret material")
	}
	path := "/api/jobs/" + job.ID
	input["version"] = job.Version
	input["secrets"] = []map[string]any{{"name": "API_TOKEN"}}
	response = h.request("PUT", path, input, cookie)
	expect(t, response, 200)
	updated := responseJob(t, response)
	input["version"] = updated.Version
	input["secrets"] = []map[string]any{{"name": "PATH", "value": "invalid"}}
	invalid := h.request("PUT", path, input, cookie)
	expect(t, invalid, 400)
	if strings.Contains(invalid.Body.String(), secretValue) {
		t.Fatal("validation exposed secret")
	}
	input["secrets"] = []map[string]any{}
	response = h.request("PUT", path, input, cookie)
	expect(t, response, 200)
	if len(responseJob(t, response).SecretNames) != 0 {
		t.Fatal("empty list did not remove secrets")
	}
}

func TestJobHTTPTagTargetsAndEmptyRunError(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	body := jobBody("")
	body["host_ids"] = []string{}
	body["host_tags"] = []string{"future"}
	w := h.request("POST", "/api/jobs", body, cookie)
	expect(t, w, 201)
	job := responseJob(t, w)
	if len(job.HostIDs) != 0 || len(job.HostTags) != 1 || job.HostTags[0] != "future" {
		t.Fatalf("lost tag-only targets: %+v", job)
	}
	w = h.request("POST", "/api/jobs/"+job.ID+"/run", map[string]any{"version": job.Version, "request_id": "fixture-intent"}, cookie)
	expect(t, w, 400)
	if !strings.Contains(w.Body.String(), "job targets must match") {
		t.Fatal("missing target error was not public", w.Body.String())
	}
	w = h.request("GET", "/api/jobs", nil, cookie)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"host_tags":["future"]`) {
		t.Fatal("job listing lost dynamic tags")
	}
}

func TestJobHTTPExclusionsAndProductionAPI(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	w := h.request("POST", "/api/hosts", hostBody(), cookie)
	expect(t, w, 201)
	host := responseHost(t, w)
	_, token := h.createPersonalKey(t, cookie, store.ScopeJobsRead, store.ScopeJobsWrite)
	input := jobBody("")
	input["host_ids"] = []string{}
	input["host_tags"] = []string{"web"}
	input["excluded_host_ids"] = []string{host.ID}
	w = h.bearerRequest("POST", "/api/v1/jobs", token, input, nil)
	expect(t, w, 201)
	job := responseJob(t, w)
	if len(job.ExcludedHostIDs) != 1 || job.ExcludedHostIDs[0] != host.ID || len(job.HostTags) != 1 {
		t.Fatalf("exclusions lost: %+v", job)
	}
	w = h.request("GET", "/api/jobs", nil, cookie)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"excluded_host_ids":["`+host.ID+`"]`) {
		t.Fatal("browser listing lost exclusions")
	}
	input["version"] = job.Version
	input["host_ids"] = []string{host.ID}
	expect(t, h.bearerRequest("PUT", "/api/v1/jobs/"+job.ID, token, input, nil), 400)
	input["host_ids"] = []string{}
	input["excluded_host_ids"] = []string{strings.Repeat("a", 32)}
	expect(t, h.bearerRequest("PUT", "/api/v1/jobs/"+job.ID, token, input, nil), 400)
	input["excluded_host_ids"] = []string{}
	w = h.bearerRequest("PUT", "/api/v1/jobs/"+job.ID, token, input, nil)
	expect(t, w, 200)
	if len(responseJob(t, w).ExcludedHostIDs) != 0 {
		t.Fatal("rechecking did not clear exclusion")
	}
}
