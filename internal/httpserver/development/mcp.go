//go:build development

package development

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"piped/internal/auth"
	"piped/internal/credentials"
	"piped/internal/hosts"
	"piped/internal/httpserver/response"
	"piped/internal/jobs"
	"piped/internal/notifications"
	"piped/internal/store"
)

type mcpCallStateKey struct{}
type mcpCallState struct{ enteredHandler bool }

// Schema validation runs before the typed handler and may include raw argument
// values in its errors. Only our handler's sanitized errors can reach a client.
func protectMCPArguments(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, request)
		}
		state := &mcpCallState{}
		result, err := next(context.WithValue(ctx, mcpCallStateKey{}, state), method, request)
		if !state.enteredHandler {
			if err != nil {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid or unavailable tool call"}
			}
			if call, ok := result.(*mcp.CallToolResult); ok && call.IsError {
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: "invalid tool arguments; check the input schema"}},
				}, nil
			}
		}
		return result, err
	}
}

func (s *server) mcpHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		actor := r.Context().Value(developmentActorKey{}).(store.Actor)
		return s.newMCPServer(actor)
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
}

func registerMCPTool[Input any](
	s *server,
	server *mcp.Server,
	actor store.Actor,
	name, description, scope string,
	destructive bool,
	execute func(context.Context, Input) (any, error),
) {
	if !store.HasScope(actor.Scopes, scope) {
		return
	}
	closedWorld := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    strings.HasSuffix(scope, ":read"),
			DestructiveHint: &destructive,
			OpenWorldHint:   &closedWorld,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		if state, ok := ctx.Value(mcpCallStateKey{}).(*mcpCallState); ok {
			state.enteredHandler = true
		}
		if err := s.config.Auth.Store.Authorize(ctx, actor, scope); err != nil {
			return nil, nil, s.mcpError(err)
		}
		output, err := execute(ctx, input)
		if err != nil {
			return nil, nil, s.mcpError(err)
		}
		return nil, output, nil
	})
}

func (s *server) mcpError(err error) error {
	switch {
	case errors.Is(err, jobs.ErrInput), errors.Is(err, store.ErrJobName),
		errors.Is(err, store.ErrJobChanged), errors.Is(err, store.ErrJobHost), errors.Is(err, store.ErrJobSecret),
		errors.Is(err, store.ErrJobNotification), errors.Is(err, store.ErrJobNotificationLimit),
		errors.Is(err, notifications.ErrInput), errors.Is(err, store.ErrNotificationType), errors.Is(err, store.ErrNotificationSecret), errors.Is(err, store.ErrNotificationChanged), errors.Is(err, store.ErrNotificationInUse),
		errors.Is(err, store.ErrHostInUse), errors.Is(err, hosts.ErrInput),
		errors.Is(err, store.ErrHostName), errors.Is(err, store.ErrHostChanged),
		errors.Is(err, store.ErrHostCredential), errors.Is(err, store.ErrHostPassword):
		return errors.New(err.Error())
	}
	err = response.ExecutionFailure(response.CredentialFailure(err))
	var public *response.Error
	if errors.As(err, &public) {
		return errors.New(public.Message)
	}
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		return errors.New("API key expired or revoked")
	case errors.Is(err, store.ErrForbidden):
		return store.ErrForbidden
	case errors.Is(err, auth.ErrUsername), errors.Is(err, auth.ErrPassword), errors.Is(err, store.ErrLastUser):
		return err
	case errors.Is(err, store.ErrConflict):
		return errors.New("username already exists")
	case errors.Is(err, store.ErrNotFound):
		return errors.New("record not found")
	default:
		s.config.Logger.Error("MCP operation failed", "error", err)
		return errors.New("operation failed")
	}
}

type emptyToolInput struct{}
type credentialToolInput struct {
	Name       string `json:"name"`
	Mode       string `json:"mode" jsonschema:"Choose generate for a new Ed25519 key or import for an existing private key"`
	PrivateKey string `json:"private_key,omitempty"`
	PublicKey  string `json:"public_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}
type credentialRenameInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type credentialIDInput struct {
	ID string `json:"id"`
}
type createUserInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type passwordToolInput struct {
	ID       int64  `json:"id"`
	Password string `json:"password"`
}
type userIDInput struct {
	ID int64 `json:"id"`
}

func (s *server) newMCPServer(actor store.Actor) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "piped-development", Version: "1.0.0"}, nil)
	server.AddReceivingMiddleware(protectMCPArguments)
	registerMCPTool(s, server, actor, "list_users", "List application users without passwords or authentication tokens.", store.ScopeUsersRead, false,
		func(ctx context.Context, _ emptyToolInput) (any, error) {
			users, err := s.config.Auth.Store.Users(ctx)
			return map[string]any{"users": users}, err
		})
	registerMCPTool(s, server, actor, "create_user", "Create an administrator account. Requires users:write.", store.ScopeUsersWrite, false,
		func(ctx context.Context, input createUserInput) (any, error) {
			return s.config.Auth.CreateUser(ctx, actor, input.Username, input.Password)
		})
	registerMCPTool(s, server, actor, "change_user_password", "Change a user's password and revoke their sessions and API keys.", store.ScopeUsersWrite, true,
		func(ctx context.Context, input passwordToolInput) (any, error) {
			if input.ID <= 0 {
				return nil, response.Fail(400, "invalid user ID")
			}
			err := s.config.Auth.ChangePassword(ctx, actor, input.ID, input.Password)
			return map[string]bool{"updated": err == nil}, err
		})
	registerMCPTool(s, server, actor, "delete_user", "Remove an administrator account and revoke their access. Cannot remove the current or last user.", store.ScopeUsersWrite, true,
		func(ctx context.Context, input userIDInput) (any, error) {
			if input.ID <= 0 {
				return nil, response.Fail(400, "invalid user ID")
			}
			if input.ID == actor.ID {
				return nil, response.Fail(409, "you cannot remove your own account")
			}
			err := s.config.Auth.Store.DeleteUser(ctx, actor, input.ID)
			return map[string]bool{"removed": err == nil}, err
		})
	s.registerNotificationMCPTools(server, actor)
	s.registerHostMCPTools(server, actor)
	s.registerJobMCPTools(server, actor)
	s.registerExecutionMCPTools(server, actor)
	if s.config.Credentials == nil {
		return server
	}
	registerMCPTool(s, server, actor, "list_credentials", "List SSH credential metadata and public keys. Private keys and passphrases are never returned.", store.ScopeCredentialsRead, false,
		func(ctx context.Context, _ emptyToolInput) (any, error) {
			items, err := s.config.Credentials.List(ctx, actor)
			return map[string]any{"credentials": items}, err
		})
	registerMCPTool(s, server, actor, "create_credential", "Generate or import an SSH key. Only public metadata is returned; private keys cannot be exported.", store.ScopeCredentialsWrite, false,
		func(ctx context.Context, input credentialToolInput) (any, error) {
			return s.config.Credentials.Create(ctx, actor, credentials.CreateInput{
				Name: input.Name, Mode: input.Mode, PrivateKey: input.PrivateKey,
				PublicKey: input.PublicKey, Passphrase: input.Passphrase,
			})
		})
	registerMCPTool(s, server, actor, "rename_credential", "Rename an SSH credential without changing its key.", store.ScopeCredentialsWrite, true,
		func(ctx context.Context, input credentialRenameInput) (any, error) {
			err := s.config.Credentials.Rename(ctx, actor, input.ID, input.Name)
			return map[string]bool{"updated": err == nil}, err
		})
	registerMCPTool(s, server, actor, "delete_credential", "Permanently remove an SSH credential.", store.ScopeCredentialsWrite, true,
		func(ctx context.Context, input credentialIDInput) (any, error) {
			err := s.config.Credentials.Delete(ctx, actor, input.ID)
			return map[string]bool{"removed": err == nil}, err
		})
	return server
}

// Host mutations return metadata only. Pointer secrets preserve an existing
// secret when omitted; an empty sudo_password explicitly removes it.
type saveHostToolInput struct {
	ID           string   `json:"id,omitempty"`
	Name         string   `json:"name"`
	Address      string   `json:"address"`
	Port         int      `json:"port"`
	Username     string   `json:"username"`
	CredentialID string   `json:"credential_id,omitempty"`
	Password     *string  `json:"password,omitempty"`
	RunAsSudo    bool     `json:"run_as_sudo"`
	SudoPassword *string  `json:"sudo_password,omitempty"`
	Tags         []string `json:"tags"`
	Version      int64    `json:"version,omitempty"`
}
type deleteHostToolInput struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

func (s *server) registerHostMCPTools(server *mcp.Server, actor store.Actor) {
	if s.config.Hosts == nil {
		return
	}
	registerMCPTool(s, server, actor, "list_hosts", "List shared SSH host settings and tags. Passwords are never returned.", store.ScopeHostsRead, false,
		func(ctx context.Context, _ emptyToolInput) (any, error) {
			items, err := s.config.Hosts.List(ctx, actor)
			return map[string]any{"hosts": items}, err
		})
	registerMCPTool(s, server, actor, "save_host", "Create or update an SSH host. Updates require id and the current version; omit password fields to preserve them. Empty sudo_password clears it. run_as_sudo enables sudo for jobs that request it. Disabling sudo removes its password. Key authentication removes the host SSH password.", store.ScopeHostsWrite, true,
		func(ctx context.Context, input saveHostToolInput) (any, error) {
			host, err := s.config.Hosts.Save(ctx, actor, input.ID, hosts.Input{
				Name: input.Name, Address: input.Address, Port: input.Port, Username: input.Username,
				CredentialID: input.CredentialID, Password: input.Password, RunAsSudo: input.RunAsSudo,
				SudoPassword: input.SudoPassword, Tags: input.Tags, Version: input.Version,
			})
			return map[string]any{"host": host}, err
		})
	registerMCPTool(s, server, actor, "delete_host", "Permanently remove a host and its passwords. Requires the current host version.", store.ScopeHostsWrite, true,
		func(ctx context.Context, input deleteHostToolInput) (any, error) {
			err := s.config.Hosts.Delete(ctx, actor, input.ID, input.Version)
			return map[string]bool{"removed": err == nil}, err
		})
}

// Job tools manage definitions only; scripts are never executed here.
type saveJobToolInput struct {
	NotificationTargetIDs []string           `json:"notification_target_ids,omitempty"`
	CopyFrom              *store.JobCopy     `json:"copy_from,omitempty"`
	Schedule              *store.JobSchedule `json:"schedule,omitempty"`
	Secrets               []jobs.SecretInput `json:"secrets,omitempty"`
	ID                    string             `json:"id,omitempty"`
	Name                  string             `json:"name"`
	Script                string             `json:"script"`
	HostIDs               []string           `json:"host_ids"`
	ExecutionMode         string             `json:"execution_mode"`
	RunAsSudo             bool               `json:"run_as_sudo,omitempty"`
	StopOnFailure         bool               `json:"stop_on_failure"`
	Version               int64              `json:"version,omitempty"`
}
type deleteJobToolInput struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

func (s *server) registerJobMCPTools(server *mcp.Server, actor store.Actor) {
	if s.config.Jobs == nil {
		return
	}
	registerMCPTool(s, server, actor, "preview_schedule", "Preview the next three matching times for a five-field cron expression in the server timezone. Does not enable scheduling or run scripts.", store.ScopeJobsRead, false,
		func(ctx context.Context, input struct {
			Cron string `json:"cron"`
		}) (any, error) {
			return s.config.Jobs.PreviewSchedule(ctx, actor, input.Cron)
		})
	registerMCPTool(s, server, actor, "list_jobs", "List shared job definitions, scripts, target host IDs, schedules, server timezone, secret names, and latest-run summaries when runs:read is granted. Secret values are never returned. Does not run jobs.", store.ScopeJobsRead, false,
		func(ctx context.Context, _ emptyToolInput) (any, error) {
			items, err := s.config.Jobs.List(ctx, actor)
			return map[string]any{"jobs": items, "server_timezone": jobs.ServerTimezone()}, err
		})
	registerMCPTool(s, server, actor, "save_job", "Create or update a job definition. Updates require id and current version. Secret values are write-only. Omit secrets to preserve them; a provided array replaces the list, with omitted values preserving named secrets. An empty array removes all secrets. Host IDs are ordered. notification_target_ids selects webhook targets; omit to preserve on edit, copy source selections on duplication, or use defaults on creation. An empty array disables notifications. run_as_sudo requests elevation only on hosts with sudo enabled. schedule uses five-field server-timezone cron. On creation, copy_from with source id/version copies encrypted secrets server-side and requires jobs:read; omitted secret values keep source values. Copies do not inherit an enabled schedule; explicitly enabling one requires runs:write. Enabled schedules also require runs:write. Omit schedule to preserve it. Does not run the script immediately.", store.ScopeJobsWrite, true,
		func(ctx context.Context, input saveJobToolInput) (any, error) {
			defer func() {
				for index := range input.Secrets {
					input.Secrets[index].Value = nil
				}
			}()
			job, err := s.config.Jobs.Save(ctx, actor, input.ID, jobs.Input{
				Name: input.Name, Script: input.Script, HostIDs: input.HostIDs, ExecutionMode: input.ExecutionMode, NotificationTargetIDs: input.NotificationTargetIDs,
				CopyFrom: input.CopyFrom, Schedule: input.Schedule, Secrets: input.Secrets, RunAsSudo: input.RunAsSudo, StopOnFailure: input.StopOnFailure, Version: input.Version,
			})
			return map[string]any{"job": job}, err
		})
	registerMCPTool(s, server, actor, "delete_job", "Permanently remove a job definition. Requires the current version.", store.ScopeJobsWrite, true,
		func(ctx context.Context, input deleteJobToolInput) (any, error) {
			err := s.config.Jobs.Delete(ctx, actor, input.ID, input.Version)
			return map[string]bool{"removed": err == nil}, err
		})
}
