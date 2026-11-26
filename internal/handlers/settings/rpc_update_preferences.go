package settings

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// The Inbox filters' setting keys (user-global, "true"/"false", default
// false). The inbox handler reads the same keys (inbox.readFilters).
const (
	inboxShowHousekeepingKey           = "inbox.show_housekeeping"
	inboxShowEveryAutomationFailureKey = "inbox.show_every_automation_failure"
)

// UpdatePreferences updates user preferences
func (s *Service) UpdatePreferences(ctx context.Context, req *connect.Request[reliantv1.UpdatePreferencesRequest]) (*connect.Response[reliantv1.UpdatePreferencesResponse], error) {
	userID := auth.MustGetUserID(ctx)
	analyticsClient := analytics.ForUser(ctx, userID)
	changedKeys := make([]string, 0, 8)
	modelProviderSettingsChanged := false
	markChanged := func(key string) {
		changedKeys = append(changedKeys, key)
		lowerKey := strings.ToLower(key)
		if strings.Contains(lowerKey, "model") || strings.Contains(lowerKey, "provider") || strings.Contains(lowerKey, "tag") {
			modelProviderSettingsChanged = true
		}
	}

	if req.Msg.StreamingEnabled != nil {
		value := "false"
		if *req.Msg.StreamingEnabled {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "features.streaming_enabled", value); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged("features.streaming_enabled")
	}

	if req.Msg.WorktreeArchiveMode != nil {
		mode := *req.Msg.WorktreeArchiveMode
		if mode != "ask_me" && mode != "always_cleanup" && mode != "always_keep" {
			return nil, svcerr.InvalidArgument("invalid worktree_archive_mode. Must be 'ask_me', 'always_cleanup', or 'always_keep'")
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.archive_cleanup_mode", mode); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged("worktree.archive_cleanup_mode")
	}

	if req.Msg.WorktreeDefaultDeleteBranch != nil {
		value := "false"
		if *req.Msg.WorktreeDefaultDeleteBranch {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.default_delete_branch", value); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged("worktree.default_delete_branch")
	}

	// Handle default auto-approve for new chats
	if req.Msg.DefaultAutoApprove != nil {
		value := "false"
		if *req.Msg.DefaultAutoApprove {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "chat.default_auto_approve", value); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged("chat.default_auto_approve")
	}

	// Handle branch copy uncommitted files default
	if req.Msg.BranchCopyUncommittedFilesDefault != nil {
		value := "false"
		if *req.Msg.BranchCopyUncommittedFilesDefault {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.branch_copy_uncommitted_files_default", value); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged("worktree.branch_copy_uncommitted_files_default")
	}

	// Handle additional arbitrary preferences
	for key, value := range req.Msg.Additional {
		prefKey := fmt.Sprintf("preference.%s", key)
		if err := s.upsertSetting(ctx, userID, nil, prefKey, value); err != nil {
			logging.ErrorContext(ctx, "Failed to update preference", "key", key, "error", err)
			// Don't fail the whole request for one preference
			continue
		}
		markChanged(prefKey)
	}

	// Handle default MCP scope
	if req.Msg.DefaultMcpScope != nil {
		scopeValue := req.Msg.DefaultMcpScope.String()
		if err := s.upsertSetting(ctx, userID, nil, "config.default_mcp_scope", scopeValue); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged("config.default_mcp_scope")
	}

	// Handle default workflow scope
	if req.Msg.DefaultWorkflowScope != nil {
		scopeValue := req.Msg.DefaultWorkflowScope.String()
		if err := s.upsertSetting(ctx, userID, nil, "config.default_workflow_scope", scopeValue); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged("config.default_workflow_scope")
	}

	// Handle default workflow
	if req.Msg.DefaultWorkflow != nil {
		if err := s.upsertSetting(ctx, userID, nil, "config.default_workflow", *req.Msg.DefaultWorkflow); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged("config.default_workflow")
	}

	// Handle hide builtin workflows
	if req.Msg.HideBuiltinWorkflows != nil {
		value := "false"
		if *req.Msg.HideBuiltinWorkflows {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "ui.hide_builtin_workflows", value); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged("ui.hide_builtin_workflows")
	}

	// Handle hide builtin presets
	if req.Msg.HideBuiltinPresets != nil {
		value := "false"
		if *req.Msg.HideBuiltinPresets {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "ui.hide_builtin_presets", value); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged("ui.hide_builtin_presets")
	}

	// The Inbox filters decide what ListInbox lists, and so the nav badge:
	// the person's clients re-read the Inbox below.
	inboxChanged := false
	for _, pref := range []struct {
		key   string
		value *bool
	}{
		{inboxShowHousekeepingKey, req.Msg.InboxShowHousekeeping},
		{inboxShowEveryAutomationFailureKey, req.Msg.InboxShowEveryAutomationFailure},
	} {
		if pref.value == nil {
			continue
		}
		value := "false"
		if *pref.value {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, pref.key, value); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preferences"))
		}
		markChanged(pref.key)
		inboxChanged = true
	}

	if len(changedKeys) > 0 {
		sort.Strings(changedKeys)
		analyticsClient.TrackPreferencesUpdated(analytics.PreferencesUpdatedMetrics{
			ChangedKeys:                  changedKeys,
			ModelProviderSettingsChanged: modelProviderSettingsChanged,
		})
	}

	if err := s.deps.Refetch.EmitUserRefetch(ctx, userID, core.RefetchConfigHealth, core.RefetchOpts{}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after UpdatePreferences", "error", err)
	}
	if inboxChanged {
		if err := s.deps.Refetch.EmitUserRefetch(ctx, userID, core.RefetchInbox, core.RefetchOpts{}); err != nil {
			logging.WarnContext(ctx, "Failed to emit inbox refetch after UpdatePreferences", "error", err)
		}
	}

	return connect.NewResponse(&reliantv1.UpdatePreferencesResponse{
		Success: true,
		Message: "Preferences updated successfully",
	}), nil
}
