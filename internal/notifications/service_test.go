package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"piped/internal/secrets"
	"piped/internal/store"
	"piped/internal/testdatabase"
)

func TestEndpointValidation(t *testing.T) {
	for _, endpoint := range []Endpoint{
		{URL: "file:///etc/passwd"},
		{URL: "https://example.internal:0/hook"},
		{URL: "https://example.internal:65536/hook"},
		{URL: "https://example.internal:999999999999999999999/hook"},
		{URL: "https://example.com", Headers: map[string]string{"Authorization": "one", "authorization": "two"}}, {URL: "https://user:password@example.com"}, {URL: "https://example.com/#secret"}, {URL: "https://example.com/\n"}, {URL: "http://"},
		{URL: "https://example.com", Headers: map[string]string{"Authorization": "token\r\nInjected: true"}},
		{URL: "https://example.com", Headers: map[string]string{"bad name": "value"}},
		{URL: "https://example.com", Headers: map[string]string{"Host": "other.example"}},
		{URL: "https://example.com", Headers: map[string]string{"Content-Length": "10000"}},
	} {
		if _, err := parseEndpoint(endpoint); err == nil {
			t.Errorf("invalid endpoint accepted")
		}
	}
	for _, address := range []string{"http://127.0.0.1:8123/hook?token=secret", "https://ntfy.example.internal/topic", "http://[::1]:8123/hook", "https://example.internal:65535/hook"} {
		if _, err := parseEndpoint(Endpoint{URL: address, Headers: map[string]string{"Authorization": "Bearer fixture"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeliveryEscapesJSONAndDoesNotFollowRedirects(t *testing.T) {
	vault, err := secrets.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	// No database needed for a pre-admitted target snapshot.
	service := &Service{vault: vault, slots: make(chan struct{}, 4), client: &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	received := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "/leaked")
			w.WriteHeader(302)
			return
		}
		if r.URL.Path == "/leaked" {
			t.Error("followed redirect")
		}
		if r.Header.Get("Authorization") != "Bearer fixture-secret" || r.Method != "PUT" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing configured method/headers")
		}
		data, _ := io.ReadAll(r.Body)
		received <- string(data)
		w.WriteHeader(204)
	}))
	defer server.Close()
	target := store.NotificationTarget{Type: "webhook", ID: "target", Method: "PUT", ContentType: "application/json", Body: `{"message":"$job on $host: $status\n$output:1"}`}
	setEndpoint := func(address string) {
		target.Endpoint = &Endpoint{URL: address, Headers: map[string]string{"Authorization": "Bearer $secret"}}
		target.Secrets = []store.NotificationSecret{{Name: "secret", Ciphertext: vault.Seal("notification:target:secret:secret", []byte("fixture-secret"))}}
	}
	setEndpoint(server.URL)
	if err := service.deliver(context.Background(), target, Event{Host: "host\"", Job: "job", Status: "failed", Output: "first\nlast\n"}); err != nil {
		t.Fatal(err)
	}
	var data map[string]string
	if err := json.Unmarshal([]byte(<-received), &data); err != nil || data["message"] != "job on host\": failed\nlast" {
		t.Fatalf("bad delivery: %v %v", data, err)
	}
	setEndpoint(server.URL + "/redirect?secret=fixture-secret")
	err = service.deliver(context.Background(), target, Event{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("unsafe failure: %v", err)
	}
	target.Type = "email"
	if err := service.deliver(context.Background(), target, Event{}); err == nil || !strings.Contains(err.Error(), "unsupported notification type") {
		t.Fatalf("unsupported type routed as webhook: %v", err)
	}
	target.Type = "webhook"
	target.ID = "other"
	if err := service.deliver(context.Background(), target, Event{}); err == nil {
		t.Fatal("ciphertext binding lost")
	}
}

func notificationFixture(t *testing.T) (*Service, *store.Store, store.Actor, *secrets.Vault) {
	t.Helper()
	ctx := context.Background()
	driver, dsn := testdatabase.Settings(t)
	db, err := store.OpenDatabase(ctx, store.Config{Driver: driver, DSN: dsn, Path: filepath.Join(t.TempDir(), "data", "piped.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := db.InitAdmin(ctx, "admin", "fixture-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, user, "fixture-session", time.Now().Unix(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(ctx, db, vault)
	if err != nil {
		t.Fatal(err)
	}
	return service, db, store.Actor{User: user, SessionHash: "fixture-session"}, vault
}

func TestUnsafeOrOversizedBodyNeverReachesWebhook(t *testing.T) {
	service, _, _, _ := notificationFixture(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	target := store.NotificationTarget{Type: "webhook", Method: "POST", Endpoint: &Endpoint{URL: server.URL}}
	target.ContentType, target.Body = "application/json", `[1$output]`
	if err := service.deliver(context.Background(), target, Event{Output: ",2"}); !errors.Is(err, ErrInput) {
		t.Fatalf("unsafe JSON body was sent: %v", err)
	}
	target.ContentType, target.Body = "text/plain", strings.Repeat("$output", 4)+"x"
	if err := service.deliver(context.Background(), target, Event{Output: strings.Repeat("x", 256*1024)}); !errors.Is(err, ErrInput) {
		t.Fatalf("oversized body was sent: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("invalid body reached the remote endpoint")
	}
}

func TestNotificationSecretsReadableTemplatesAndAtomicPreservation(t *testing.T) {
	service, _, actor, _ := notificationFixture(t)
	ctx := context.Background()
	value := "private-token"
	input := Input{Type: "webhook", Name: "Hook", Method: "POST", ContentType: "application/json", Body: `{"text":"$job: $secret"}`, Enabled: true,
		Endpoint: &Endpoint{URL: "https://example.internal/hook/$secret?token=$secret", Headers: map[string]string{"Authorization": "Bearer $secret"}}, Secrets: []SecretInput{{Name: "secret", Value: &value}}}
	target, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(ctx, actor)
	if err != nil || len(listed) != 1 || listed[0].Endpoint.URL != input.Endpoint.URL || listed[0].Endpoint.Headers["Authorization"] != "Bearer $secret" || len(listed[0].SecretNames) != 1 || listed[0].SecretNames[0] != "secret" {
		t.Fatalf("unreadable templates: %+v %v", listed, err)
	}
	encoded, _ := json.Marshal(listed)
	if bytes.Contains(encoded, []byte(value)) || len(listed[0].Secrets) != 0 {
		t.Fatal("secret value exposed")
	}
	input.Version, input.Endpoint, input.Secrets = target.Version, nil, []SecretInput{{Name: "secret"}}
	target, err = service.Save(ctx, actor, target.ID, input)
	if err != nil || target.Endpoint.URL != listed[0].Endpoint.URL {
		t.Fatalf("preservation failed: %v", err)
	}
	input.Version, input.Secrets = target.Version, []SecretInput{}
	if _, err := service.Save(ctx, actor, target.ID, input); !errors.Is(err, ErrInput) {
		t.Fatalf("removed referenced secret: %v", err)
	}
	listed, err = service.List(ctx, actor)
	if err != nil || listed[0].Version != target.Version || len(listed[0].SecretNames) != 1 {
		t.Fatal("invalid removal did not roll back")
	}
	input.Endpoint = &Endpoint{URL: "https://example.internal", Headers: map[string]string{}}
	input.Body = `{"text":"$job"}`
	target, err = service.Save(ctx, actor, target.ID, input)
	if err != nil || len(target.SecretNames) != 0 {
		t.Fatalf("secret removal failed: %v", err)
	}
}

func TestNotificationSecretInputValidation(t *testing.T) {
	value := "value"
	bad := "\x00"
	for _, input := range [][]SecretInput{
		{{Name: "host", Value: &value}}, {{Name: "job", Value: &value}}, {{Name: "1token", Value: &value}}, {{Name: "bad-name", Value: &value}}, {{Name: "token", Value: &bad}}, {{Name: "token", Value: &value}, {Name: "token", Value: &value}},
	} {
		if err := validateSecretInputs(input); err == nil {
			t.Fatal("invalid secret input accepted")
		}
	}
}

func TestNotificationEndpointSecretEscapingAndInjection(t *testing.T) {
	token := "a/b ?&#=+%$other"
	template := Endpoint{URL: "https://example.internal/fixed/$token?token=$token&static=yes", Headers: map[string]string{"Authorization": "Bearer $token"}}
	rendered, err := renderEndpoint(template, map[string]string{"token": token})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(rendered.URL)
	if err != nil || parsed.Host != "example.internal" || parsed.EscapedPath() != "/fixed/"+url.PathEscape(token) || parsed.Query().Get("token") != token || len(parsed.Query()) != 2 || rendered.Headers["Authorization"] != "Bearer "+token {
		t.Fatalf("unsafe substitution: %+v %v", rendered, err)
	}
	for _, endpoint := range []Endpoint{
		{URL: "https://$token.example.internal/path"}, {URL: "https://example.internal/$missing"}, {URL: "https://example.internal", Headers: map[string]string{"Authorization": "$token"}},
	} {
		if _, err := renderEndpoint(endpoint, map[string]string{"token": "value\r\nInjected: yes"}); err == nil {
			t.Fatal("unsafe or unknown secret accepted")
		}
	}
	body, err := Render(`{"value":"$token"}`, "application/json", Event{Secrets: map[string]string{"token": "\"\\\n$job"}})
	var decoded map[string]string
	if err != nil || json.Unmarshal([]byte(body), &decoded) != nil || decoded["value"] != "\"\\\n$job" {
		t.Fatal("secret body escaping or single-pass substitution failed")
	}
}

func TestNotificationReservedAndDuplicateNamesIgnoreCase(t *testing.T) {
	value := "value"
	for _, name := range []string{"HOST", "Host", "jOb", "STATUS", "OuTpUt"} {
		if err := validateSecretInputs([]SecretInput{{Name: name, Value: &value}}); err == nil {
			t.Errorf("reserved name accepted: %s", name)
		}
	}
	if err := validateSecretInputs([]SecretInput{{Name: "Token", Value: &value}, {Name: "TOKEN", Value: &value}}); err == nil {
		t.Fatal("case-only duplicate names accepted")
	}
}

func TestNotificationSecretEditsIgnoreCaseWithoutBreakingCiphertext(t *testing.T) {
	service, _, actor, _ := notificationFixture(t)
	ctx := context.Background()
	original := "MiXeD-Original-Value"
	input := Input{Type: "webhook", Name: "Hook", Method: "POST", ContentType: "text/plain", Body: "$Api_Token $HOST $JoB $STATUS $OUTPUT:1", Enabled: true,
		Endpoint: &Endpoint{URL: "https://example.internal/$API_TOKEN", Headers: map[string]string{"Authorization": "Bearer $api_token"}},
		Secrets:  []SecretInput{{Name: "Api_Token", Value: &original}},
	}
	target, err := service.Save(ctx, actor, "", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Version, input.Secrets = target.Version, []SecretInput{{Name: "aPI_tOKEN"}}
	target, err = service.Save(ctx, actor, target.ID, input)
	if err != nil || len(target.SecretNames) != 1 || target.SecretNames[0] != "Api_Token" {
		t.Fatalf("case-only preservation changed encrypted name binding: %+v %v", target, err)
	}
	replacement := "MiXeD-Replacement-Value"
	input.Version, input.Secrets = target.Version, []SecretInput{{Name: "API_TOKEN", Value: &replacement}}
	target, err = service.Save(ctx, actor, target.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	// A further preservation requires opening the ciphertext saved under the new
	// spelling, proving replacements also retain the correct authenticated name.
	input.Version, input.Secrets = target.Version, []SecretInput{{Name: "api_token"}}
	target, err = service.Save(ctx, actor, target.ID, input)
	if err != nil || target.SecretNames[0] != "API_TOKEN" {
		t.Fatalf("replacement lost its encrypted name binding: %+v %v", target, err)
	}
}

func TestNotificationStoredNameConflictsFailClosed(t *testing.T) {
	service, _, _, vault := notificationFixture(t)
	for _, names := range [][]string{{"HOST"}, {"Token", "TOKEN"}} {
		target := store.NotificationTarget{ID: "fixture"}
		for _, name := range names {
			target.Secrets = append(target.Secrets, store.NotificationSecret{Name: name, Ciphertext: vault.Seal("notification:fixture:secret:"+name, []byte("private"))})
		}
		if _, err := service.secretValues(target); !errors.Is(err, ErrInput) {
			t.Fatalf("ambiguous stored names accepted: %v", err)
		}
	}
}
