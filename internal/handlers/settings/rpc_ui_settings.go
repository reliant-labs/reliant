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

	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"

	"github.com/reliant-labs/reliant/internal/workflow"
)

// ============================================================================
// Sub-phase 6b: UI Settings (Shortcuts/Preferences)
// ============================================================================

// GetShortcuts retrieves user keyboard shortcuts
func (s *Service) GetShortcuts(ctx context.Context, req *connect.Request[reliantv1.GetShortcutsRequest]) (*connect.Response[reliantv1.GetShortcutsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	setting, err := s.deps.Repo.GetSetting(ctx, userID, nil, "shortcuts")
	if err != nil {
		// Return empty shortcuts if not found (frontend will use defaults)
		return connect.NewResponse(&reliantv1.GetShortcutsResponse{
			Shortcuts: "",
		}), nil
	}

	return connect.NewResponse(&reliantv1.GetShortcutsResponse{
		Shortcuts: setting.Value,
	}), nil
}

// UpdateShortcuts updates user keyboard shortcuts
func (s *Service) UpdateShortcuts(ctx context.Context, req *connect.Request[reliantv1.UpdateShortcutsRequest]) (*connect.Response[reliantv1.UpdateShortcutsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.upsertSetting(ctx, userID, nil, "shortcuts", req.Msg.Shortcuts); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update shortcuts"))
	}

	return connect.NewResponse(&reliantv1.UpdateShortcutsResponse{
		Success: true,
		Message: "Shortcuts updated successfully",
	}), nil
}

// GetPreferences retrieves user preferences
func (s *Service) GetPreferences(ctx context.Context, req *connect.Request[reliantv1.GetPreferencesRequest]) (*connect.Response[reliantv1.GetPreferencesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Every preference below lives in the same user-global scope
	// (project_id IS NULL), so one query answers all of them. Reading them
	// one key at a time meant eleven serial round trips, each queueing for a
	// connection from the pool — the dominant cost of this RPC.
	values := make(map[string]string)
	additional := make(map[string]string)

	// Keys that are surfaced as dedicated proto fields rather than in
	// Additional, so they are not returned twice.
	excludedPrefKeys := map[string]bool{
		"default_planning_mode": true,
		"default_auto_approve":  true,
	}

	allSettings, err := s.deps.Repo.ListSettings(ctx, userID, nil)
	if err == nil {
		for _, setting := range allSettings {
			// ListSettings orders by (key, updated_at), so on the off chance a
			// duplicate row survives, the newest value overwrites the older.
			values[setting.Key] = setting.Value
			if len(setting.Key) > 11 && setting.Key[:11] == "preference." {
				key := setting.Key[11:] // Remove "preference." prefix
				if !excludedPrefKeys[key] {
					additional[key] = setting.Value
				}
			}
		}
	}

	// boolPref keeps the previous semantics exactly: a missing key falls back
	// to the default, and any stored value other than "true" is false.
	boolPref := func(key string, defaultVal bool) bool {
		if value, ok := values[key]; ok {
			return value == "true"
		}
		return defaultVal
	}
	scopePref := func(key string) reliantv1.ConfigScope {
		if value, ok := values[key]; ok {
			if scope, ok := reliantv1.ConfigScope_value[value]; ok {
				return reliantv1.ConfigScope(scope)
			}
		}
		return reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT
	}

	streamingEnabled := boolPref("features.streaming_enabled", true)
	worktreeDeleteBranch := boolPref("worktree.default_delete_branch", false)
	branchCopyUncommittedFilesDefault := boolPref("worktree.branch_copy_uncommitted_files_default", false)
	hideBuiltinWorkflows := boolPref("ui.hide_builtin_workflows", false)
	hideBuiltinPresets := boolPref("ui.hide_builtin_presets", false)

	worktreeArchiveMode := "ask_me"
	if value, ok := values["worktree.archive_cleanup_mode"]; ok {
		worktreeArchiveMode = value
	}

	defaultMcpScope := scopePref("config.default_mcp_scope")
	defaultWorkflowScope := scopePref("config.default_workflow_scope")

	// An empty stored value is ignored rather than blanking the default.
	defaultWorkflow := workflow.DefaultWorkflow
	if value := values["config.default_workflow"]; value != "" {
		defaultWorkflow = value
	}

	// Compute effective hidden items by combining defaults and user overrides
	hiddenWorkflows, err := s.getEffectiveHiddenItems(ctx, userID, int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW))
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get hidden workflows"))
	}
	hiddenPresets, err := s.getEffectiveHiddenItems(ctx, userID, int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET))
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get hidden presets"))
	}

	return connect.NewResponse(&reliantv1.GetPreferencesResponse{
		StreamingEnabled:                  streamingEnabled,
		WorktreeArchiveMode:               worktreeArchiveMode,
		WorktreeDefaultDeleteBranch:       worktreeDeleteBranch,
		Additional:                        additional,
		BranchCopyUncommittedFilesDefault: branchCopyUncommittedFilesDefault,
		DefaultMcpScope:                   defaultMcpScope,
		DefaultWorkflowScope:              defaultWorkflowScope,
		DefaultWorkflow:                   defaultWorkflow,
		HideBuiltinWorkflows:              hideBuiltinWorkflows,
		HideBuiltinPresets:                hideBuiltinPresets,
		HiddenWorkflowSlugs:               hiddenWorkflows,
		HiddenPresetSlugs:                 hiddenPresets,
	}), nil
}

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

	if len(changedKeys) > 0 {
		sort.Strings(changedKeys)
		analyticsClient.TrackPreferencesUpdated(analytics.PreferencesUpdatedMetrics{
			ChangedKeys:                  changedKeys,
			ModelProviderSettingsChanged: modelProviderSettingsChanged,
		})
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after UpdatePreferences", "error", err)
	}

	return connect.NewResponse(&reliantv1.UpdatePreferencesResponse{
		Success: true,
		Message: "Preferences updated successfully",
	}), nil
}

// SetHiddenItem toggles the hidden state of a workflow or preset
func (s *Service) SetHiddenItem(ctx context.Context, req *connect.Request[reliantv1.SetHiddenItemRequest]) (*connect.Response[reliantv1.SetHiddenItemResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Validate item type
	if req.Msg.ItemType != reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW && req.Msg.ItemType != reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET {
		return nil, svcerr.InvalidArgument("item_type must be 'workflow' or 'preset'")
	}

	// Validate slug
	if req.Msg.Slug == "" {
		return nil, svcerr.InvalidArgument("slug cannot be empty")
	}

	// Update visibility override
	// hidden=true means is_visible=false, hidden=false means is_visible=true
	isVisible := !req.Msg.Hidden
	if err := s.deps.Repo.SetVisibilityOverride(ctx, userID, int32(req.Msg.ItemType), req.Msg.Slug, isVisible); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update hidden state"))
	}

	action := "hidden"
	if !req.Msg.Hidden {
		action = "shown"
	}

	return connect.NewResponse(&reliantv1.SetHiddenItemResponse{
		Success: true,
		Message: fmt.Sprintf("%s %s successfully %s", req.Msg.ItemType, req.Msg.Slug, action),
	}), nil
}

// getEffectiveHiddenItems computes the list of hidden items for a user.
// This combines system defaults (items hidden by default) with user overrides.
func (s *Service) getEffectiveHiddenItems(ctx context.Context, userID string, itemType int32) ([]string, error) {
	hiddenSet := make(map[string]bool)

	// Start with items hidden by default
	defaults, err := s.deps.Repo.ListHiddenItemDefaults(ctx, itemType)
	if err != nil {
		return nil, fmt.Errorf("failed to list hidden item defaults: %w", err)
	}
	for _, slug := range defaults {
		hiddenSet[slug] = true
	}

	// Apply user overrides
	overrides, err := s.deps.Repo.ListVisibilityOverrides(ctx, userID, itemType)
	if err != nil {
		return nil, fmt.Errorf("failed to list visibility overrides: %w", err)
	}
	for slug, isVisible := range overrides {
		if isVisible {
			// User explicitly made this visible
			delete(hiddenSet, slug)
		} else {
			// User explicitly hid this
			hiddenSet[slug] = true
		}
	}

	// Convert to slice
	result := make([]string, 0, len(hiddenSet))
	for slug := range hiddenSet {
		result = append(result, slug)
	}
	return result, nil
}
