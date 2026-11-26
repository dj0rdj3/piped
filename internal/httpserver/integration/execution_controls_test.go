package integration

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/execution"
	"piped/internal/store"
)

func TestLiveExecutionControlsThroughBrowserAndPersonalAPI(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	var authentications atomic.Int32
	configuration := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
		authentications.Add(1)
		return nil, errors.New("fixture rejects authentication")
	}}
	configuration.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { listener.Close(); <-done })
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return
		}
		_, _, _, _ = ssh.NewServerConn(connection, configuration)
	}()
	target := hostBody()
	target["address"], target["port"] = "127.0.0.1", listener.Addr().(*net.TCPAddr).Port
	w := h.request("POST", "/api/hosts", target, cookie)
	expect(t, w, 201)
	host := responseHost(t, w)
	w = h.request("POST", "/api/jobs", jobBody(host.ID), cookie)
	expect(t, w, 201)
	job := responseJob(t, w)
	_, readToken := h.createPersonalKey(t, cookie, store.ScopeRunsRead)
	_, writeToken := h.createPersonalKey(t, cookie, store.ScopeRunsWrite)
	_, hostToken := h.createPersonalKey(t, cookie, store.ScopeHostsWrite)
	revokedKey, revokedToken := h.createPersonalKey(t, cookie, store.ScopeRunsWrite)
	expect(t, h.request("DELETE", "/api/api-keys/"+revokedKey.ID, map[string]string{"current_password": testPassword}, cookie), 204)
	w = h.request("POST", "/api/jobs/"+job.ID+"/run", map[string]any{"version": job.Version, "request_id": "control-authority"}, cookie)
	expect(t, w, 201)
	var view execution.RunView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	runPath := "/runs/" + view.Run.ID
	hostPath := runPath + "/hosts/" + host.ID
	server := httptest.NewServer(h.handler)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 3 * time.Second}
	request := func(method, path, token string, browser bool, origin string, body any, status int) []byte {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r, err := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		r.Host = strings.TrimPrefix(h.origin, "http://")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Piped-Request", "1")
		if browser {
			r.AddCookie(cookie)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, response.StatusCode, status, data)
		}
		return data
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		data := request("GET", "/api"+runPath, "", true, h.origin, nil, 200)
		if err := json.Unmarshal(data, &view); err != nil {
			t.Fatal(err)
		}
		if len(view.HostKeys) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SSH fixture did not reach host-key approval")
		}
		time.Sleep(time.Millisecond)
	}
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	if view.HostKeys[0].Fingerprint != fingerprint {
		t.Fatal("unexpected pending host key")
	}
	for _, action := range []struct {
		path string
		body any
	}{
		{"/input", map[string]string{"data": "must not reach SSH"}},
		{"/cancel", nil},
		{"/host-key", map[string]any{"fingerprint": fingerprint, "accept": false}},
		{"/host-key", map[string]any{"fingerprint": fingerprint, "accept": true}},
	} {
		request("POST", "/api"+hostPath+action.path, "", false, h.origin, action.body, 401)
		request("POST", "/api"+hostPath+action.path, "", true, "https://attacker.example", action.body, 403)
		request("POST", "/api/v1"+hostPath+action.path, readToken, false, "", action.body, 403)
		request("POST", "/api/v1"+hostPath+action.path, hostToken, false, "", action.body, 403)
		request("POST", "/api/v1"+hostPath+action.path, revokedToken, false, "", action.body, 401)
	}
	request("POST", "/api/v1"+hostPath+"/host-key", writeToken, false, "", map[string]any{"fingerprint": fingerprint, "accept": true}, 403)
	data := request("GET", "/api"+runPath, "", true, h.origin, nil, 200)
	if err := json.Unmarshal(data, &view); err != nil || len(view.HostKeys) != 1 || view.Run.Hosts[0].Status != "awaiting_approval" {
		t.Fatal("rejected control changed the live execution")
	}
	request("POST", "/api/v1"+hostPath+"/host-key", writeToken, false, "", map[string]any{"fingerprint": fingerprint, "accept": false}, 204)
	deadline = time.Now().Add(3 * time.Second)
	for {
		data = request("GET", "/api"+runPath, "", true, h.origin, nil, 200)
		if err := json.Unmarshal(data, &view); err != nil {
			t.Fatal(err)
		}
		if view.Run.FinishedAt != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authorized host-key denial did not finish the run")
		}
		time.Sleep(time.Millisecond)
	}
	if view.Run.Status != "failed" || !strings.Contains(view.Run.Hosts[0].Message, execution.ErrHostKeyDenied.Error()) || authentications.Load() != 0 {
		t.Fatal("host-key denial did not prevent SSH authentication")
	}
}
