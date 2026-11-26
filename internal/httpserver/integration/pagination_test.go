package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"piped/internal/store"
	"piped/web"
)

// Set PIPED_TABLE_SCALE_TEST=1 for the 500-resource stress fixture. The normal
// suite uses the same creation/listing paths with a smaller dataset.
func TestResourceTablePagination(t *testing.T) {
	count := 12
	if os.Getenv("PIPED_TABLE_SCALE_TEST") == "1" {
		count = 500
	}
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	actor, err := h.auth.Resolve(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < count; index++ {
		name := fmt.Sprintf("Fixture %04d", index)
		if index > 0 {
			// Application account/key services exercise authorization and validation
			// without repeatedly reauthenticating or disabling HTTP rate limits.
			if _, err := h.auth.CreateUser(context.Background(), actor, fmt.Sprintf("fixture%04d", index), testPassword); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := h.auth.CreateUserAPIKey(context.Background(), actor, name, []string{store.ScopeHostsRead}, time.Hour); err != nil {
			t.Fatal(err)
		}
		created := h.request("POST", "/api/credentials", map[string]any{"mode": "generate", "name": name}, cookie)
		expect(t, created, 201)
		var credential struct {
			Credential store.Credential `json:"credential"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &credential); err != nil {
			t.Fatal(err)
		}
		created = h.request("POST", "/api/hosts", map[string]any{"name": name, "address": "127.0.0.1", "port": 22, "username": "fixture", "credential_id": credential.Credential.ID, "tags": []string{fmt.Sprintf("group%d", index%2)}}, cookie)
		expect(t, created, 201)
		host := responseHost(t, created)
		created = h.request("POST", "/api/jobs", map[string]any{"name": name, "script": "true", "host_ids": []string{host.ID}, "execution_mode": "sequential"}, cookie)
		expect(t, created, 201)
		created = h.request("POST", "/api/notifications", map[string]any{"type": "webhook", "name": name, "method": "POST", "content_type": "text/plain", "body": "$job: $status", "enabled": false, "endpoint": map[string]any{"url": "https://example.invalid/fixture"}}, cookie)
		expect(t, created, 201)
	}
	for _, resource := range []struct{ path, field string }{
		{"hosts", "hosts"}, {"jobs", "jobs"}, {"credentials", "credentials"}, {"users", "users"}, {"notifications", "targets"}, {"api-keys", "keys"},
	} {
		t.Run(resource.path, func(t *testing.T) {
			seen := map[string]bool{}
			cursor := ""
			for {
				w := h.request("GET", "/api/"+resource.path+"?limit=7&cursor="+url.QueryEscape(cursor), nil, cookie)
				expect(t, w, 200)
				var body map[string]json.RawMessage
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				var page struct {
					Total, Matched int
					Next           string `json:"next_cursor"`
				}
				if err := json.Unmarshal(body["page"], &page); err != nil {
					t.Fatal(err)
				}
				if page.Total != count || page.Matched != count {
					t.Fatalf("counts: %+v", page)
				}
				var items []struct {
					ID json.RawMessage `json:"id"`
				}
				if err := json.Unmarshal(body[resource.field], &items); err != nil {
					t.Fatal(err)
				}
				if len(items) == 0 || len(items) > 7 {
					t.Fatalf("page length %d", len(items))
				}
				for _, item := range items {
					id := string(item.ID)
					if seen[id] {
						t.Fatalf("duplicate ID: %s", id)
					}
					seen[id] = true
				}
				if page.Next == "" {
					break
				}
				if page.Next == cursor {
					t.Fatal("cursor did not advance")
				}
				cursor = page.Next
			}
			if len(seen) != count {
				t.Fatalf("got %d of %d", len(seen), count)
			}
			result := h.request("GET", "/api/"+resource.path+"?limit=8&q="+fmt.Sprintf("%04d", count-1), nil, cookie)
			expect(t, result, 200)
			var filtered struct{ Page struct{ Total, Matched int } }
			if err := json.Unmarshal(result.Body.Bytes(), &filtered); err != nil {
				t.Fatal(err)
			}
			if filtered.Page.Total != count || filtered.Page.Matched != 1 {
				t.Fatalf("full-collection search: %s", result.Body.String())
			}
			invalidIDCursor := base64.RawURLEncoding.EncodeToString([]byte(`{"key":"valid","id":"bad\u0000id"}`))
			for _, query := range []string{"limit=0", "limit=201", "limit=invalid", "limit=1&cursor=invalid", "cursor=invalid", "limit=1&cursor=" + invalidIDCursor} {
				expect(t, h.request("GET", "/api/"+resource.path+"?"+query, nil, cookie), 400)
			}
			expect(t, h.request("GET", "/api/"+resource.path+"?limit=8", nil, nil), 401)
			t.Logf("Listed and searched all %d records with unique, advancing cursors", count)
		})
	}
	tags := h.request("GET", "/api/hosts?limit=1&tag=group1", nil, cookie)
	expect(t, tags, 200)
	var filtered struct {
		Tags []string
		Page struct{ Total, Matched int }
	}
	if err := json.Unmarshal(tags.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered.Tags) != 2 || filtered.Page.Matched != count/2 {
		t.Fatalf("tag coverage: %s", tags.Body.String())
	}

	if stopFile := os.Getenv("PIPED_TABLE_BROWSER_STOP_FILE"); stopFile != "" {
		previewTableFixture(t, h, stopFile)
	}
}

// Optional loopback browser preview uses only this test's disposable database.
// Touch the supplied stop file to finish; the bounded timeout also cleans it up.
func previewTableFixture(t *testing.T, h *harness, stopFile string) {
	t.Helper()
	assets, err := web.Assets()
	if err != nil {
		t.Fatal(err)
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	files := http.FileServer(http.FS(assets))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/mcp" {
			h.handler.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	t.Log("500-resource browser fixture ready at http://127.0.0.1:8080; sign in with test admin")
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(stopFile); err == nil {
				return
			}
		case <-deadline.C:
			t.Fatal("browser fixture timed out")
		case <-t.Context().Done():
			t.Fatal("browser fixture cancelled")
		}
	}
}

func TestJobPaginationSearchRespectsTargetsAndHostReadScope(t *testing.T) {
	h := newHarness(t, false)
	cookie := h.login(t, "admin", testPassword)
	actor, err := h.auth.Resolve(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	var hosts []store.Host
	for index, name := range []string{"Explicit match", "Tagged match", "Excluded match"} {
		input := hostBody()
		input["name"] = name
		input["tags"] = []string{}
		if index > 0 {
			input["tags"] = []string{"search-tag"}
		}
		created := h.request("POST", "/api/hosts", input, cookie)
		expect(t, created, 201)
		hosts = append(hosts, responseHost(t, created))
	}
	input := jobBody(hosts[0].ID)
	input["name"] = "Search job"
	input["host_tags"] = []string{"search-tag"}
	input["excluded_host_ids"] = []string{hosts[2].ID}
	expect(t, h.request("POST", "/api/jobs", input, cookie), 201)
	for _, withHosts := range []bool{false, true} {
		scopes := []string{store.ScopeJobsRead}
		if withHosts {
			scopes = append(scopes, store.ScopeHostsRead)
		}
		key, token, err := h.auth.CreateUserAPIKey(context.Background(), actor, "Search fixture", scopes, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{"Explicit", "Tagged", "Excluded", "Search job", "search-tag"} {
			path := "/jobs?limit=1&q=" + url.QueryEscape(query)
			for _, bearer := range []bool{false, true} {
				result := h.request("GET", "/api"+path, nil, cookie)
				if bearer {
					result = h.bearerRequest("GET", "/api/v1"+path, token, nil, nil)
				}
				expect(t, result, 200)
				var body struct {
					Jobs []store.Job
					Page struct{ Matched int }
				}
				if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				want := 1
				if query == "Excluded" || bearer && !withHosts && (query == "Explicit" || query == "Tagged") {
					want = 0
				}
				if len(body.Jobs) != want || body.Page.Matched != want {
					t.Fatalf("query=%q bearer=%v hosts:read=%v: %s", query, bearer, withHosts, result.Body.String())
				}
			}
		}
		if err := h.store.RevokeUserAPIKey(context.Background(), actor, key.ID); err != nil {
			t.Fatal(err)
		}
	}
}
