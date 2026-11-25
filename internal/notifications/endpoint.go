package notifications

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func parseEndpoint(endpoint Endpoint) (*url.URL, error) {
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || len(endpoint.URL) > 8192 || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" || parsed.Scheme != "http" && parsed.Scheme != "https" || strings.ContainsFunc(endpoint.URL, unicode.IsControl) {
		return nil, invalid("provide an HTTP or HTTPS webhook URL without user information or a fragment")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, invalid("webhook URL port must be between 1 and 65535")
		}
	}
	if len(endpoint.Headers) > 32 {
		return nil, invalid("provide at most 32 headers")
	}
	total := 0
	seen := map[string]bool{}
	for name, value := range endpoint.Headers {
		canonicalName := strings.ToLower(name)
		if seen[canonicalName] {
			return nil, invalid("header names must be unique ignoring case")
		}
		seen[canonicalName] = true
		total += len(name) + len(value)
		if len(name) == 0 || len(name) > 100 || len(value) > 8192 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return nil, invalid("provide valid header names and values without control characters")
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
				return nil, invalid("provide valid HTTP header names")
			}
		}
		switch canonicalName {
		case "host", "content-length", "transfer-encoding", "connection", "trailer", "upgrade", "proxy-authorization", "proxy-connection", "content-type":
			return nil, invalid("transport and Content-Type headers cannot be overridden; use the body format setting")
		}
	}
	if total > 32*1024 {
		return nil, invalid("headers must total at most 32 KiB")
	}
	return parsed, nil
}

func templateNameEnd(text string, index int) int {
	for index < len(text) && (text[index] >= 'a' && text[index] <= 'z' || text[index] >= 'A' && text[index] <= 'Z' || text[index] >= '0' && text[index] <= '9' || text[index] == '_') {
		index++
	}
	return index
}

// Expand once and escape secret values for the component receiving them. Static
// URL syntax stays intact; a secret cannot change the destination or inject a
// path segment, query parameter, fragment, or HTTP header.
func expandSecrets(text string, values map[string]string, escape func(string) string) (string, error) {
	var result strings.Builder
	for index := 0; index < len(text); {
		if text[index] != '$' {
			result.WriteByte(text[index])
			index++
			continue
		}
		index++
		if index < len(text) && text[index] == '$' {
			result.WriteByte('$')
			index++
			continue
		}
		start := index
		index = templateNameEnd(text, index)
		value, exists := lookupSecret(values, text[start:index])
		if !exists {
			return "", invalid("endpoint template references an unknown secret; use $name or $$ for a literal dollar")
		}
		result.WriteString(escape(value))
		if result.Len() > 32*1024 {
			return "", invalid("expanded endpoint setting exceeds 32 KiB")
		}
	}
	return result.String(), nil
}

func renderEndpoint(endpoint Endpoint, values map[string]string) (Endpoint, error) {
	parsed, err := parseEndpoint(endpoint)
	if err != nil {
		return Endpoint{}, err
	}
	if strings.Contains(parsed.Scheme, "$") || strings.Contains(parsed.Host, "$") {
		return Endpoint{}, invalid("URL secrets may appear only in the path or query")
	}
	path, err := expandSecrets(parsed.EscapedPath(), values, url.PathEscape)
	if err != nil {
		return Endpoint{}, err
	}
	parsed.Path, err = url.PathUnescape(path)
	if err != nil {
		return Endpoint{}, invalid("invalid URL path")
	}
	parsed.RawPath = path
	parsed.RawQuery, err = expandSecrets(parsed.RawQuery, values, url.QueryEscape)
	if err != nil {
		return Endpoint{}, err
	}
	rendered := Endpoint{URL: parsed.String(), Headers: map[string]string{}}
	for name, value := range endpoint.Headers {
		rendered.Headers[name], err = expandSecrets(value, values, func(value string) string { return value })
		if err != nil {
			return Endpoint{}, err
		}
	}
	if _, err := parseEndpoint(rendered); err != nil {
		return Endpoint{}, err
	}
	return rendered, nil
}
