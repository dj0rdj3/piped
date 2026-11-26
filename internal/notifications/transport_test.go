package notifications

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"piped/internal/secrets"
	"piped/internal/store"
)

func transportTarget(vault *secrets.Vault, address, value string) store.NotificationTarget {
	return store.NotificationTarget{
		Type: "webhook", ID: "transport-fixture", Method: http.MethodPost,
		ContentType: "text/plain", Body: "$token",
		Endpoint: &Endpoint{URL: address, Headers: map[string]string{"X-Webhook-Token": "$token"}},
		Secrets: []store.NotificationSecret{{
			Name: "token", Ciphertext: vault.Seal("notification:transport-fixture:secret:token", []byte(value)),
		}},
	}
}

func TestWebhookClientNeverReplaysSecretsThroughRedirects(t *testing.T) {
	service, _, _, vault := notificationFixture(t)
	var redirected atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()

	const secret = "redirect-fixture-secret"
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Header.Get("X-Webhook-Token") != secret || r.URL.Query().Get("token") != secret {
			t.Error("the configured endpoint did not receive its secret")
		}
		if r.Method != http.MethodGet && string(body) != secret {
			t.Error("the configured endpoint did not receive its rendered body")
		}
		status, err := strconv.Atoi(r.URL.Query().Get("status"))
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Location", destination.URL+"/unexpected?token="+secret)
		w.WriteHeader(status)
	}))
	defer source.Close()

	// Exercise the client returned by New, rather than replacing it with a test
	// client that independently supplies the redirect protection under test.
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("%s/%d", method, status), func(t *testing.T) {
				target := transportTarget(vault, source.URL+"/?status="+strconv.Itoa(status)+"&token=$token", secret)
				target.Method = method
				if method == http.MethodGet {
					target.Body = ""
				}
				err := service.deliver(context.Background(), target, Event{})
				if err == nil || err.Error() != fmt.Sprintf("webhook returned HTTP %d", status) {
					t.Fatalf("redirect was followed or returned unsafe diagnostics: %v", err)
				}
				if redirected.Load() != 0 {
					t.Fatal("webhook secrets reached a redirect destination")
				}
			})
		}
	}
}

func TestWebhookClientVerifiesCertificateAndHostnameBeforeSendingSecrets(t *testing.T) {
	service, _, _, vault := notificationFixture(t)
	var requests atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Webhook-Token") != "tls-fixture-secret" {
			t.Error("trusted endpoint did not receive its configured secret")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	target := transportTarget(vault, server.URL, "tls-fixture-secret")
	const safeError = "webhook request failed; check endpoint, TLS, and connectivity"
	if err := service.deliver(context.Background(), target, Event{}); err == nil || err.Error() != safeError {
		t.Fatalf("untrusted certificate was accepted or exposed transport diagnostics: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("secrets were sent before certificate verification")
	}

	transport := service.client.Transport.(*http.Transport)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	address, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	target.Endpoint.URL = "https://wrong-webhook.invalid:" + address.Port()
	if err := service.deliver(context.Background(), target, Event{}); err == nil || err.Error() != safeError {
		t.Fatalf("wrong certificate hostname was accepted or exposed diagnostics: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("secrets were sent before hostname verification")
	}

	target.Endpoint.URL = server.URL
	if err := service.deliver(context.Background(), target, Event{}); err != nil {
		t.Fatal("valid certificate and hostname were rejected", err)
	}
	if requests.Load() != 1 {
		t.Fatal("expected exactly one delivery to the trusted endpoint")
	}
}

func TestWebhookClientIgnoresAmbientProxies(t *testing.T) {
	const childFlag = "PIPED_WEBHOOK_PROXY_TEST"
	if os.Getenv(childFlag) != "1" {
		// net/http caches proxy environment settings process-wide. A separate
		// process makes this independent of other tests using DefaultTransport.
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestWebhookClientIgnoresAmbientProxies$", "-test.count=1")
		command.Env = append(os.Environ(), childFlag+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("ambient proxy isolation failed: %v\n%s", err, output)
		}
		return
	}

	var proxyRequests atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
		t.Setenv(name, proxy.URL)
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}

	var directRequests atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directRequests.Add(1)
		if r.Header.Get("X-Webhook-Token") != "proxy-fixture-secret" {
			t.Error("direct endpoint did not receive its configured secret")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()
	service, _, _, vault := notificationFixture(t)
	transport := service.client.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "webhook.invalid:80" {
			address = destination.Listener.Addr().String()
		} else if address != proxy.Listener.Addr().String() {
			return nil, fmt.Errorf("unexpected fixture destination %q", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	target := transportTarget(vault, "http://webhook.invalid/hook", "proxy-fixture-secret")
	err := service.deliver(context.Background(), target, Event{})
	if proxyRequests.Load() != 0 {
		t.Fatal("webhook secrets reached an ambient proxy")
	}
	if err != nil || directRequests.Load() != 1 {
		t.Fatal("webhook did not reach its configured endpoint directly", err)
	}
}
