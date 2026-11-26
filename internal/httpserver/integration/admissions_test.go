package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func containsRunCount(t *testing.T, response *httptest.ResponseRecorder, expected int) bool {
	t.Helper()
	expect(t, response, 200)
	var result struct {
		Runs []json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return len(result.Runs) == expected
}

func TestHTTPAdmissionIdentityReplaysAcrossBodyAndHeader(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	hostInput := hostBody()
	hostInput["address"], hostInput["port"] = "127.0.0.1", 1
	host := responseHost(t, h.request("POST", "/api/hosts", hostInput, cookie))
	job := responseJob(t, h.request("POST", "/api/jobs", jobBody(host.ID), cookie))
	path := "/api/jobs/" + job.ID + "/run"
	expect(t, h.request("POST", path, map[string]any{"version": job.Version}, cookie), 400)
	first := h.request("POST", path, map[string]any{"version": job.Version, "request_id": "same-reviewed-start"}, cookie)
	expect(t, first, 201)
	second := h.request("POST", path, map[string]any{"version": job.Version, "request_id": "same-reviewed-start"}, cookie)
	expect(t, second, 201)
	if runs := h.request("GET", "/api/jobs/"+job.ID+"/runs", nil, cookie); !containsRunCount(t, runs, 1) {
		t.Fatal("repeated HTTP start admitted multiple runs")
	}
	expect(t, h.request("POST", path, map[string]any{"version": job.Version + 1, "request_id": "same-reviewed-start"}, cookie), 409)
	expect(t, h.request("POST", path, map[string]any{"version": job.Version, "request_id": "bad key"}, cookie), 400)
	expect(t, h.request("POST", path, map[string]any{"version": job.Version, "job_id": "different"}, cookie), 400)
	_, token := h.createPersonalKey(t, cookie, "runs:write")
	response := h.bearerRequest("POST", "/api/v1/jobs/"+job.ID+"/run", token, map[string]any{"version": job.Version}, func(request *http.Request) {
		request.Header.Set("Idempotency-Key", "same-reviewed-start")
	})
	expect(t, response, 201)
	response = h.bearerRequest("POST", "/api/v1/jobs/"+job.ID+"/run", token, map[string]any{"version": job.Version, "request_id": "other"}, func(request *http.Request) {
		request.Header.Set("Idempotency-Key", "same-reviewed-start")
	})
	expect(t, response, 400)
	if runs := h.request("GET", "/api/jobs/"+job.ID+"/runs", nil, cookie); !containsRunCount(t, runs, 1) {
		t.Fatal("header replay created another run")
	}
}
