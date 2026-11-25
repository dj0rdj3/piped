//go:build !development

package development

import (
	"net/http"

	"piped/internal/httpserver/response"
)

const Enabled = false

func Register(*http.ServeMux, Config, []response.Route, response.Endpoint) {}

func KeyRequest(string) bool { return false }
