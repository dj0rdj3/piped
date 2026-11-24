package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestClientIPTrustBoundary(t *testing.T) {
	s := &Server{config: Config{TrustedProxies: []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}}}
	for _, fixture := range []struct {
		name, peer string
		forwarded  []string
		real       []string
		want       string
	}{
		{name: "direct peer", peer: "198.51.100.9:1234", want: "198.51.100.9"},
		{name: "untrusted peer", peer: "198.51.100.9:1234", forwarded: []string{"203.0.113.5"}, real: []string{"203.0.113.6"}, want: "198.51.100.9"},
		{name: "trusted peer without headers", peer: "127.0.0.1:1234", want: "127.0.0.1"},
		{name: "forwarded client", peer: "127.0.0.2:1234", forwarded: []string{"203.0.113.5"}, want: "203.0.113.5"},
		{name: "real IP fallback", peer: "127.0.0.1:1234", real: []string{" 203.0.113.5 "}, want: "203.0.113.5"},
		{name: "forwarded takes precedence", peer: "127.0.0.1:1234", forwarded: []string{"203.0.113.5"}, real: []string{"203.0.113.6"}, want: "203.0.113.5"},
		{name: "trusted chain", peer: "127.0.0.1:1234", forwarded: []string{"203.0.113.5, 10.0.0.2, 127.0.0.3"}, want: "203.0.113.5"},
		{name: "spoofed prefix", peer: "127.0.0.1:1234", forwarded: []string{"192.0.2.99, 203.0.113.5, 10.0.0.2"}, want: "203.0.113.5"},
		{name: "invalid ignored prefix", peer: "127.0.0.1:1234", forwarded: []string{"invalid, 203.0.113.5, 10.0.0.2"}, want: "127.0.0.1"},
		{name: "zoned ignored prefix", peer: "127.0.0.1:1234", forwarded: []string{"fe80::1%eth0, 203.0.113.5, 10.0.0.2"}, want: "127.0.0.1"},
		{name: "empty ignored prefix", peer: "127.0.0.1:1234", forwarded: []string{", 203.0.113.5, 10.0.0.2"}, want: "127.0.0.1"},
		{name: "untrusted nearest hop", peer: "127.0.0.1:1234", forwarded: []string{"192.0.2.99, 203.0.113.5"}, want: "203.0.113.5"},
		{name: "repeated forwarded fields", peer: "127.0.0.1:1234", forwarded: []string{"192.0.2.99", "203.0.113.5, 10.0.0.2"}, want: "203.0.113.5"},
		{name: "all hops trusted", peer: "127.0.0.1:1234", forwarded: []string{"10.0.0.2, 127.0.0.3"}, want: "10.0.0.2"},
		{name: "IPv6", peer: "[::1]:1234", forwarded: []string{"2001:db8::5"}, want: "2001:db8::5"},
		{name: "IPv4 mapped peer", peer: "[::ffff:127.0.0.1]:1234", forwarded: []string{"::ffff:203.0.113.5"}, want: "203.0.113.5"},
		{name: "IPv6 normalization", peer: "[2001:0db8::5]:1234", want: "2001:db8::5"},
		{name: "peer without port", peer: "127.0.0.1", real: []string{"203.0.113.5"}, want: "203.0.113.5"},
		{name: "invalid peer", peer: "unknown", real: []string{"203.0.113.5"}, want: "unknown"},
		{name: "empty forwarded", peer: "127.0.0.1:1234", forwarded: []string{""}, real: []string{"203.0.113.5"}, want: "127.0.0.1"},
		{name: "invalid forwarded", peer: "127.0.0.1:1234", forwarded: []string{"unknown"}, real: []string{"203.0.113.5"}, want: "127.0.0.1"},
		{name: "empty hop", peer: "127.0.0.1:1234", forwarded: []string{"203.0.113.5,,10.0.0.2"}, want: "127.0.0.1"},
		{name: "forwarded port", peer: "127.0.0.1:1234", forwarded: []string{"203.0.113.5:5678"}, want: "127.0.0.1"},
		{name: "forwarded zone", peer: "127.0.0.1:1234", forwarded: []string{"fe80::1%eth0"}, want: "127.0.0.1"},
		{name: "invalid real IP", peer: "127.0.0.1:1234", real: []string{"203.0.113.5, 203.0.113.6"}, want: "127.0.0.1"},
		{name: "repeated real IP", peer: "127.0.0.1:1234", real: []string{"203.0.113.5", "203.0.113.6"}, want: "127.0.0.1"},
		{name: "real IP zone", peer: "127.0.0.1:1234", real: []string{"fe80::1%eth0"}, want: "127.0.0.1"},
		{name: "too many hops", peer: "127.0.0.1:1234", forwarded: []string{strings.Repeat("10.0.0.2,", 32) + "203.0.113.5"}, want: "127.0.0.1"},
		{name: "oversized forwarded field", peer: "127.0.0.1:1234", forwarded: []string{strings.Repeat(" ", 4096) + "203.0.113.5"}, want: "127.0.0.1"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
			r.RemoteAddr = fixture.peer
			for _, value := range fixture.forwarded {
				r.Header.Add("X-Forwarded-For", value)
			}
			for _, value := range fixture.real {
				r.Header.Add("X-Real-IP", value)
			}
			if got := s.clientIP(r); got != fixture.want {
				t.Fatalf("client IP = %q, want %q", got, fixture.want)
			}
			if r.RemoteAddr != fixture.peer {
				t.Fatal("forwarded client IP changed the socket peer")
			}
		})
	}

	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.5")
	s.config.TrustedProxies = nil
	if s.clientIP(r) != "127.0.0.1" {
		t.Fatal("empty trusted-proxy configuration accepted forwarded headers")
	}
}

func TestTrustedProxyConfigRejectsInvalidPrefixes(t *testing.T) {
	err := ValidateConfig(Config{
		Listen: "127.0.0.1:8080", Origin: "http://127.0.0.1:8080",
		TrustedProxies: []netip.Prefix{{}},
	})
	if err == nil {
		t.Fatal("invalid trusted proxy prefix accepted")
	}
}
