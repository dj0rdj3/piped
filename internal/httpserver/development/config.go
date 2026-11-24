// Package development contains internal transports excluded from production.
package development

import (
	"log/slog"
	"net/http"
	"time"

	"piped/internal/auth"
	"piped/internal/credentials"
	"piped/internal/execution"
	"piped/internal/hosts"
	"piped/internal/jobs"
)

type Config struct {
	Origin, Listen string
	Auth           *auth.Service
	Credentials    *credentials.Service
	Hosts          *hosts.Service
	Jobs           *jobs.Service
	Executions     *execution.Engine
	Logger         *slog.Logger
	Allow          func(string, time.Time) bool
	ClientIP       func(*http.Request) string
}
