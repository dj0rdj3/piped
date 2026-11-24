package integration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"piped/internal/jobs"
)

func TestSchedulePreviewAuthenticationAndValidation(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	expect(t, h.request("GET", "/api/jobs/schedule-preview?cron=*%20*%20*%20*%20*", nil, nil), 401)
	for _, expression := range []string{"", "@daily", "60%20*%20*%20*%20*"} {
		expect(t, h.request("GET", "/api/jobs/schedule-preview?cron="+expression, nil, cookie), 400)
	}
	before := time.Now()
	w := h.request("GET", "/api/jobs/schedule-preview?cron=*%20*%20*%20*%20*", nil, cookie)
	expect(t, w, 200)
	var result jobs.SchedulePreview
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Occurrences) != 3 || result.ServerTimezone == "" || !result.Occurrences[0].After(before) {
		t.Fatalf("invalid preview: %+v", result)
	}
	if result.Occurrences[1].Sub(result.Occurrences[0]) != time.Minute || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("incorrect preview or caching")
	}
	w = h.request("GET", "/api/jobs/schedule-preview?cron=0%200%2030%202%20*", nil, cookie)
	expect(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Occurrences) != 0 {
		t.Fatal("impossible date preview")
	}
}

func TestCopyJobHTTPAndPausedCronEditing(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	hostResponse := h.request("POST", "/api/hosts", hostBody(), cookie)
	expect(t, hostResponse, 201)
	input := jobBody(responseHost(t, hostResponse).ID)
	input["secrets"] = []map[string]any{{"name": "TOKEN", "value": "copy-http-private-value"}}
	input["schedule"] = map[string]any{"cron": "0 2 * * *", "enabled": true}
	response := h.request("POST", "/api/jobs", input, cookie)
	expect(t, response, 201)
	source := responseJob(t, response)
	input["name"] = "HTTP duplicate"
	input["copy_from"] = map[string]any{"id": source.ID, "version": source.Version}
	input["secrets"] = []map[string]any{{"name": "TOKEN"}}
	input["schedule"] = map[string]any{"cron": "30 3 * * *", "enabled": false}
	response = h.request("POST", "/api/jobs", input, cookie)
	expect(t, response, 201)
	copy := responseJob(t, response)
	if copy.ID == source.ID || len(copy.SecretNames) != 1 || copy.Schedule.Enabled || copy.Schedule.Cron != "30 3 * * *" || strings.Contains(response.Body.String(), "copy-http-private-value") {
		t.Fatal("copy, paused cron editing, or write-only secrets failed")
	}
	input["name"] = "Stale duplicate"
	input["copy_from"] = map[string]any{"id": source.ID, "version": source.Version + 1}
	expect(t, h.request("POST", "/api/jobs", input, cookie), 409)
	input["copy_from"] = map[string]any{"id": source.ID, "version": source.Version}
	input["version"] = copy.Version
	expect(t, h.request("PUT", "/api/jobs/"+copy.ID, input, cookie), 400)
}
