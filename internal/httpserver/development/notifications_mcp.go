//go:build development

package development

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"piped/internal/notifications"
	"piped/internal/store"
)

type notificationToolInput struct {
	ID       string              `json:"id,omitempty"`
	Settings notifications.Input `json:"settings"`
}

func (s *server) registerNotificationMCPTools(server *mcp.Server, actor store.Actor) {
	if s.config.Notifications == nil {
		return
	}
	registerMCPTool(s, server, actor, "list_notification_targets", "List webhook target metadata and templates. URL and headers are readable; secret values are never returned.", store.ScopeNotificationsRead, false,
		func(ctx context.Context, _ emptyToolInput) (any, error) {
			targets, err := s.config.Notifications.List(ctx, actor)
			return map[string]any{"targets": targets}, err
		})
	registerMCPTool(s, server, actor, "save_notification_target", "Create or update a notification target. settings.type is required and selects the notification type (currently webhook only). Target type is immutable. Omit id to create; updates require settings.version. settings.endpoint contains readable URL and header templates; omit it on edit to preserve both. settings.secrets contains named write-only values: omit to preserve the list; a supplied array replaces it, omitted values preserve existing names, and an empty array removes all. Secret names and template variables are case-insensitive; host/job/status/output are reserved in every case and case-only duplicate names are rejected. Reference a secret named token as $token in the body, URL path/query, or header values. default_enabled preselects this enabled target for new jobs. Body variables: $host, $job, $status, $output, $output:10, $$. JSON variables belong inside strings.", store.ScopeNotificationsWrite, true,
		func(ctx context.Context, input notificationToolInput) (any, error) {
			defer func() {
				for index := range input.Settings.Secrets {
					input.Settings.Secrets[index].Value = nil
				}
			}()
			target, err := s.config.Notifications.Save(ctx, actor, input.ID, input.Settings)
			return map[string]any{"target": target}, err
		})
	registerMCPTool(s, server, actor, "delete_notification_target", "Remove an unassigned notification target using its id and current version.", store.ScopeNotificationsWrite, true,
		func(ctx context.Context, input deleteJobToolInput) (any, error) {
			err := s.config.Notifications.Delete(ctx, actor, input.ID, input.Version)
			return map[string]bool{"removed": err == nil}, err
		})
}
