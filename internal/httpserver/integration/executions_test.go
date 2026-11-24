package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"piped/internal/store"
)

func TestExecutionHTTPAuthorizationHistoryAndValidation(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	targetBody := hostBody()
	targetBody["address"] = "127.0.0.1"
	targetBody["port"] = 1
	w := h.request("POST", "/api/hosts", targetBody, cookie)
	expect(t, w, 201)
	target := responseHost(t, w)
	w = h.request("POST", "/api/jobs", jobBody(target.ID), cookie)
	expect(t, w, 201)
	job := responseJob(t, w)
	path := "/api/jobs/" + job.ID + "/run"
	expect(t, h.request("POST", path, map[string]int{"version": 1}, nil), 401)
	cross := httptest.NewRequest("POST", path, strings.NewReader(`{"version":1}`))
	cross.AddCookie(cookie)
	cross.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, cross)
	expect(t, response, 403)
	expect(t, h.request("POST", path, map[string]int{"version": 2}, cookie), 409)
	expect(t, h.request("POST", path, map[string]int{"version": 0}, cookie), 400)
	w = h.request("POST", path, map[string]int{"version": 1}, cookie)
	expect(t, w, 201)
	var result struct {
		Run store.Run `json:"run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Run.JobID != job.ID || len(result.Run.Hosts) != 1 {
		t.Fatal("incomplete run response")
	}
	runPath := "/api/runs/" + result.Run.ID
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w = h.request("GET", runPath, nil, cookie)
		expect(t, w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Run.FinishedAt > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result.Run.Status != "failed" || result.Run.FinishedAt == 0 {
		t.Fatalf("connection failure not persisted: %+v", result.Run)
	}
	if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "encrypted_") {
		t.Fatal("execution response exposed secrets")
	}
	expect(t, h.request("GET", runPath, nil, nil), 401)
	w = h.request("GET", "/api/jobs/"+job.ID+"/runs", nil, cookie)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), result.Run.ID) {
		t.Fatal("history did not retain run")
	}
	hostPath := runPath + "/hosts/" + target.ID
	expect(t, h.request("GET", hostPath+"/output?after=-1", nil, cookie), 400)
	expect(t, h.request("GET", hostPath+"/output?after=NaN", nil, cookie), 400)
	expect(t, h.request("GET", hostPath+"/output", nil, cookie), 200)
	expect(t, h.request("POST", hostPath+"/input", map[string]string{"data": ""}, cookie), 400)
	expect(t, h.request("POST", hostPath+"/input", map[string]string{"data": strings.Repeat("x", 4097)}, cookie), 400)
	expect(t, h.request("POST", hostPath+"/input", map[string]string{"data": "hello\n"}, cookie), 409)
	expect(t, h.request("POST", hostPath+"/cancel", nil, cookie), 409)
	expect(t, h.request("POST", hostPath+"/host-key", map[string]string{"fingerprint": "stale"}, cookie), 400)
	expect(t, h.request("GET", "/api/runs/missing", nil, cookie), 404)
	expect(t, h.request("GET", runPath+"/hosts/missing/output", nil, cookie), 404)
}

func TestExecutionTailIsBoundedAndPreservesCursorHistory(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	hostResponse := h.request("POST", "/api/hosts", hostBody(), cookie)
	expect(t, hostResponse, 201)
	host := responseHost(t, hostResponse)
	jobResponse := h.request("POST", "/api/jobs", jobBody(host.ID), cookie)
	expect(t, jobResponse, 201)
	job := responseJob(t, jobResponse)
	actor, err := h.auth.Resolve(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	run, _, _, err := h.store.CreateRun(context.Background(), actor, "tail-fixture", job.ID, job.Version)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/runs/" + run.ID + "/hosts/" + host.ID + "/output"
	expect(t, h.request("GET", path+"?tail=1", nil, nil), 401)
	expect(t, h.request("GET", path+"?tail=1&after=0", nil, cookie), 400)
	expect(t, h.request("GET", path+"?tail=yes", nil, cookie), 400)
	expect(t, h.request("GET", "/api/runs/missing/hosts/missing/output?tail=1", nil, cookie), 404)
	var result struct {
		Chunks    []store.OutputChunk `json:"chunks"`
		Truncated bool                `json:"truncated"`
	}
	response := h.request("GET", path+"?tail=1", nil, cookie)
	expect(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Truncated || len(result.Chunks) != 0 {
		t.Fatal("empty tail incorrect")
	}
	// More than 16 chunks and 256 KiB, with a partial oldest included chunk.
	var all bytes.Buffer
	for index := 0; index < 100; index++ {
		data := bytes.Repeat([]byte{byte('A' + index%26)}, 4001)
		all.Write(data)
		if err := h.store.AppendOutput(context.Background(), run.ID, host.ID, data); err != nil {
			t.Fatal(err)
		}
	}
	response = h.request("GET", path+"?tail=1", nil, cookie)
	expect(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var tail bytes.Buffer
	var lastID int64
	for _, chunk := range result.Chunks {
		if chunk.ID <= lastID {
			t.Fatal("tail not in cursor order")
		}
		tail.Write(chunk.Data)
		lastID = chunk.ID
	}
	if !result.Truncated || !bytes.Equal(tail.Bytes(), all.Bytes()[all.Len()-256*1024:]) {
		t.Fatal("tail replayed old output, lost data, or exceeded its bound")
	}
	if err := h.store.AppendOutput(context.Background(), run.ID, host.ID, []byte("new output")); err != nil {
		t.Fatal(err)
	}
	response = h.request("GET", path+"?after="+strconv.FormatInt(lastID, 10), nil, cookie)
	expect(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Chunks) != 1 || string(result.Chunks[0].Data) != "new output" {
		t.Fatal("tail cursor missed new output")
	}
	response = h.request("GET", path+"?after=0", nil, cookie)
	expect(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Chunks) != 16 || !bytes.Equal(result.Chunks[0].Data, all.Bytes()[:4001]) {
		t.Fatal("full-history cursor changed")
	}
}
