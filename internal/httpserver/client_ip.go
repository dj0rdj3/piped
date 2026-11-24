package httpserver

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// clientIP resolves only the client address. RemoteAddr remains the actual peer;
// forwarded headers never change host, origin, or development access checks.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.WithZone("").Unmap()
	if !s.isTrustedProxy(peer) {
		return peer.String()
	}

	if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		chain := strings.Join(values, ",")
		if len(chain) > 4096 {
			return peer.String()
		}
		entries := strings.Split(chain, ",")
		if len(entries) > 32 {
			return peer.String()
		}
		// Validate every hop, including ignored prefixes. Consume addresses only
		// while the nearest hop is trusted; its untrusted prefix cannot set the IP.
		client := peer
		for index := len(entries) - 1; index >= 0; index-- {
			address, err := netip.ParseAddr(strings.TrimSpace(entries[index]))
			if err != nil || address.Zone() != "" {
				return peer.String()
			}
			if s.isTrustedProxy(client) {
				client = address.Unmap()
			}
		}
		return client.String()
	}

	if values := r.Header.Values("X-Real-IP"); len(values) == 1 {
		address, err := netip.ParseAddr(strings.TrimSpace(values[0]))
		if err == nil && address.Zone() == "" {
			return address.Unmap().String()
		}
	}
	return peer.String()
}

func (s *Server) isTrustedProxy(address netip.Addr) bool {
	for _, prefix := range s.config.TrustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
