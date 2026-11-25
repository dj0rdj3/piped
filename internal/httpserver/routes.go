package httpserver

import (
	"piped/internal/httpserver/response"
	"piped/internal/store"
)

// Browser and bearer APIs share handlers and resource availability.
// Their authentication wrappers remain separate; only bearer keys use scopes.
func (s *Server) resourceRoutes() []response.Route {
	routes := []response.Route{
		{Method: "GET", Path: "/users", Scope: store.ScopeUsersRead, Handler: s.users},
		{Method: "POST", Path: "/users", Scope: store.ScopeUsersWrite, Handler: s.createUser},
		{Method: "DELETE", Path: "/users/{id}", Scope: store.ScopeUsersWrite, Handler: s.deleteUser},
		{Method: "POST", Path: "/users/{id}/password", Scope: store.ScopeUsersWrite, Handler: s.changePassword},
	}
	if s.config.Credentials != nil {
		routes = append(routes,
			response.Route{Method: "GET", Path: "/credentials", Scope: store.ScopeCredentialsRead, Handler: s.listCredentials},
			response.Route{Method: "POST", Path: "/credentials", Scope: store.ScopeCredentialsWrite, Handler: s.createCredential},
			response.Route{Method: "PATCH", Path: "/credentials/{id}", Scope: store.ScopeCredentialsWrite, Handler: s.renameCredential},
			response.Route{Method: "DELETE", Path: "/credentials/{id}", Scope: store.ScopeCredentialsWrite, Handler: s.deleteCredential},
		)
	}
	if s.config.Hosts != nil {
		routes = append(routes,
			response.Route{Method: "GET", Path: "/hosts", Scope: store.ScopeHostsRead, Handler: s.listHosts},
			response.Route{Method: "POST", Path: "/hosts", Scope: store.ScopeHostsWrite, Handler: s.saveHost},
			response.Route{Method: "PUT", Path: "/hosts/{id}", Scope: store.ScopeHostsWrite, Handler: s.saveHost},
			response.Route{Method: "DELETE", Path: "/hosts/{id}", Scope: store.ScopeHostsWrite, Handler: s.deleteHost},
		)
	}
	if s.config.Notifications != nil {
		routes = append(routes,
			response.Route{Method: "GET", Path: "/notifications", Scope: store.ScopeNotificationsRead, Handler: s.listNotifications},
			response.Route{Method: "POST", Path: "/notifications", Scope: store.ScopeNotificationsWrite, Handler: s.saveNotification},
			response.Route{Method: "PUT", Path: "/notifications/{id}", Scope: store.ScopeNotificationsWrite, Handler: s.saveNotification},
			response.Route{Method: "DELETE", Path: "/notifications/{id}", Scope: store.ScopeNotificationsWrite, Handler: s.deleteNotification},
		)
	}
	if s.config.Jobs != nil {
		routes = append(routes,
			response.Route{Method: "GET", Path: "/jobs", Scope: store.ScopeJobsRead, Handler: s.listJobs},
			response.Route{Method: "GET", Path: "/jobs/schedule-preview", Scope: store.ScopeJobsRead, Handler: s.schedulePreview},
			response.Route{Method: "POST", Path: "/jobs", Scope: store.ScopeJobsWrite, Handler: s.saveJob},
			response.Route{Method: "PUT", Path: "/jobs/{id}", Scope: store.ScopeJobsWrite, Handler: s.saveJob},
			response.Route{Method: "DELETE", Path: "/jobs/{id}", Scope: store.ScopeJobsWrite, Handler: s.deleteJob},
		)
	}
	if s.config.Executions != nil {
		routes = append(routes,
			response.Route{Method: "POST", Path: "/jobs/{id}/run", Scope: store.ScopeRunsWrite, Handler: s.startRun},
			response.Route{Method: "GET", Path: "/jobs/{id}/runs", Scope: store.ScopeRunsRead, Handler: s.listRuns},
			response.Route{Method: "GET", Path: "/runs/{run}", Scope: store.ScopeRunsRead, Handler: s.getRun},
			response.Route{Method: "GET", Path: "/runs/{run}/hosts/{host}/output", Scope: store.ScopeRunsRead, Handler: s.runOutput},
			response.Route{Method: "POST", Path: "/runs/{run}/hosts/{host}/input", Scope: store.ScopeRunsWrite, Handler: s.runInput},
			response.Route{Method: "POST", Path: "/runs/{run}/hosts/{host}/cancel", Scope: store.ScopeRunsWrite, Handler: s.cancelRunHost},
			response.Route{Method: "POST", Path: "/runs/{run}/hosts/{host}/host-key", Scope: store.ScopeRunsWrite, Handler: s.approveRunHost},
		)
	}
	return routes
}
