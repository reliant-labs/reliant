// Copyright (c) 2025 Reliant Labs
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/copilot"
	"github.com/reliant-labs/reliant/internal/llm/models"

	"github.com/reliant-labs/reliant/internal/logging"

	"github.com/reliant-labs/reliant/internal/validation"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// ============================================================================
// Helper Methods
// ============================================================================

// settingToProto converts a db.Setting to proto Setting
func settingToProto(s *db.Setting) *reliantv1.Setting {
	setting := &reliantv1.Setting{
		Id:        s.ID,
		Key:       s.Key,
		Value:     s.Value,
		ValueType: s.ValueType,
		CreatedAt: s.CreatedAt.Format(time.RFC3339),
		UpdatedAt: s.UpdatedAt.Format(time.RFC3339),
	}
	if s.ProjectID != nil {
		setting.ProjectId = s.ProjectID
	}
	return setting
}

// upsertSetting creates or updates a setting
func (s *Service) upsertSetting(ctx context.Context, userID string, projectID *string, key, value string) error {
	setting, err := s.deps.Repo.GetSetting(ctx, userID, projectID, key)
	if err != nil {
		if !isSettingNotFoundError(err) {
			return err
		}

		// Setting doesn't exist, create it
		newSetting := &db.Setting{
			ID:        uuid.New().String(),
			UserID:    userID,
			ProjectID: projectID,
			Key:       key,
			Value:     value,
			ValueType: "string",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		return s.deps.Repo.CreateSetting(ctx, newSetting)
	}

	// Setting exists, update it
	setting.Value = value
	setting.UpdatedAt = time.Now().UTC()
	return s.deps.Repo.UpdateSetting(ctx, setting)
}

func isSettingNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "setting not found")
}

// ============================================================================
// Sub-phase 6a: Core Settings CRUD
// ============================================================================

// CreateSetting creates a new setting
func (s *Service) CreateSetting(ctx context.Context, req *connect.Request[reliantv1.CreateSettingRequest]) (*connect.Response[reliantv1.CreateSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}
	if req.Msg.Value == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("value is required"))
	}

	valueType := "string"
	if req.Msg.ValueType != nil {
		valueType = *req.Msg.ValueType
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	now := time.Now().UTC()
	setting := &db.Setting{
		ID:        uuid.New().String(),
		UserID:    userID,
		ProjectID: projectID,
		Key:       req.Msg.Key,
		Value:     req.Msg.Value,
		ValueType: valueType,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.deps.Repo.CreateSetting(ctx, setting); err != nil {
		logging.ErrorContext(ctx, "Failed to create setting", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create setting"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after CreateSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.CreateSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}

// ListSettings lists all settings for the current user
func (s *Service) ListSettings(ctx context.Context, req *connect.Request[reliantv1.ListSettingsRequest]) (*connect.Response[reliantv1.ListSettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	settings, err := s.deps.Repo.ListSettings(ctx, userID, projectID)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to list settings", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list settings"))
	}

	protoSettings := make([]*reliantv1.Setting, len(settings))
	for i, setting := range settings {
		protoSettings[i] = settingToProto(setting)
	}

	return connect.NewResponse(&reliantv1.ListSettingsResponse{
		Settings: protoSettings,
		Total:    int32(len(protoSettings)),
	}), nil
}

// GetSetting retrieves a setting by key
func (s *Service) GetSetting(ctx context.Context, req *connect.Request[reliantv1.GetSettingRequest]) (*connect.Response[reliantv1.GetSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.deps.Repo.GetSetting(ctx, userID, projectID, req.Msg.Key)
	if err != nil {
		// Return empty response instead of error for missing settings.
		// This prevents frontend callers from throwing when a setting
		// simply hasn't been created yet.
		return connect.NewResponse(&reliantv1.GetSettingResponse{
			Setting: nil,
		}), nil
	}

	return connect.NewResponse(&reliantv1.GetSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}

// UpdateSetting updates an existing setting
func (s *Service) UpdateSetting(ctx context.Context, req *connect.Request[reliantv1.UpdateSettingRequest]) (*connect.Response[reliantv1.UpdateSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.deps.Repo.GetSetting(ctx, userID, projectID, req.Msg.Key)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("setting not found"))
	}

	if req.Msg.Value != nil {
		setting.Value = *req.Msg.Value
	}
	if req.Msg.ValueType != nil {
		setting.ValueType = *req.Msg.ValueType
	}
	setting.UpdatedAt = time.Now().UTC()

	if err := s.deps.Repo.UpdateSetting(ctx, setting); err != nil {
		logging.ErrorContext(ctx, "Failed to update setting", "error", err, "key", req.Msg.Key)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update setting"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after UpdateSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.UpdateSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}

// BatchUpsertSettings writes many settings in one round trip.
//
// Each entry goes through CreateSetting, which is an upsert server-side (see
// the ON CONFLICT note in queries/settings.sql), so a batch does not have to
// know which keys already exist. One refetch is emitted for the whole batch
// rather than one per key — the fan-out this RPC exists to remove would
// otherwise just reappear on the event side.
func (s *Service) BatchUpsertSettings(ctx context.Context, req *connect.Request[reliantv1.BatchUpsertSettingsRequest]) (*connect.Response[reliantv1.BatchUpsertSettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	for _, write := range req.Msg.Settings {
		if write.GetKey() == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required for every setting in a batch"))
		}
	}

	now := time.Now().UTC()
	written := make([]*reliantv1.Setting, 0, len(req.Msg.Settings))

	for _, write := range req.Msg.Settings {
		valueType := "string"
		if write.ValueType != nil && *write.ValueType != "" {
			valueType = *write.ValueType
		}

		setting := &db.Setting{
			ID:        uuid.New().String(),
			UserID:    userID,
			ProjectID: projectID,
			Key:       write.GetKey(),
			Value:     write.GetValue(),
			ValueType: valueType,
			CreatedAt: now,
			UpdatedAt: now,
		}

		if err := s.deps.Repo.CreateSetting(ctx, setting); err != nil {
			logging.ErrorContext(ctx, "Failed to upsert setting in batch", "error", err, "key", write.GetKey())
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to upsert setting %q", write.GetKey()))
		}

		written = append(written, settingToProto(setting))
	}

	if len(written) > 0 {
		if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
			logging.WarnContext(ctx, "Failed to emit config_health refetch after BatchUpsertSettings", "error", err)
		}
	}

	return connect.NewResponse(&reliantv1.BatchUpsertSettingsResponse{
		Settings: written,
	}), nil
}

// DeleteSetting deletes a setting by key
func (s *Service) DeleteSetting(ctx context.Context, req *connect.Request[reliantv1.DeleteSettingRequest]) (*connect.Response[reliantv1.DeleteSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.deps.Repo.GetSetting(ctx, userID, projectID, req.Msg.Key)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("setting not found"))
	}

	if err := s.deps.Repo.DeleteSetting(ctx, setting.ID); err != nil {
		logging.ErrorContext(ctx, "Failed to delete setting", "error", err, "key", req.Msg.Key)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete setting"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after DeleteSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.DeleteSettingResponse{
		Success: true,
		Message: "Setting deleted successfully",
	}), nil
}

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
		logging.ErrorContext(ctx, "Failed to update shortcuts", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update shortcuts"))
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
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get hidden workflows: %w", err))
	}
	hiddenPresets, err := s.getEffectiveHiddenItems(ctx, userID, int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get hidden presets: %w", err))
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
			logging.ErrorContext(ctx, "Failed to update streaming_enabled", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("features.streaming_enabled")
	}

	if req.Msg.WorktreeArchiveMode != nil {
		mode := *req.Msg.WorktreeArchiveMode
		if mode != "ask_me" && mode != "always_cleanup" && mode != "always_keep" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid worktree_archive_mode. Must be 'ask_me', 'always_cleanup', or 'always_keep'"))
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.archive_cleanup_mode", mode); err != nil {
			logging.ErrorContext(ctx, "Failed to update worktree.archive_cleanup_mode", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("worktree.archive_cleanup_mode")
	}

	if req.Msg.WorktreeDefaultDeleteBranch != nil {
		value := "false"
		if *req.Msg.WorktreeDefaultDeleteBranch {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.default_delete_branch", value); err != nil {
			logging.ErrorContext(ctx, "Failed to update worktree.default_delete_branch", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
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
			logging.ErrorContext(ctx, "Failed to update chat.default_auto_approve", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
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
			logging.ErrorContext(ctx, "Failed to update worktree.branch_copy_uncommitted_files_default", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
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
			logging.ErrorContext(ctx, "Failed to update config.default_mcp_scope", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("config.default_mcp_scope")
	}

	// Handle default workflow scope
	if req.Msg.DefaultWorkflowScope != nil {
		scopeValue := req.Msg.DefaultWorkflowScope.String()
		if err := s.upsertSetting(ctx, userID, nil, "config.default_workflow_scope", scopeValue); err != nil {
			logging.ErrorContext(ctx, "Failed to update config.default_workflow_scope", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("config.default_workflow_scope")
	}

	// Handle default workflow
	if req.Msg.DefaultWorkflow != nil {
		if err := s.upsertSetting(ctx, userID, nil, "config.default_workflow", *req.Msg.DefaultWorkflow); err != nil {
			logging.ErrorContext(ctx, "Failed to update config.default_workflow", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
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
			logging.ErrorContext(ctx, "Failed to update ui.hide_builtin_workflows", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
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
			logging.ErrorContext(ctx, "Failed to update ui.hide_builtin_presets", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
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
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_type must be 'workflow' or 'preset'"))
	}

	// Validate slug
	if req.Msg.Slug == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("slug cannot be empty"))
	}

	// Update visibility override
	// hidden=true means is_visible=false, hidden=false means is_visible=true
	isVisible := !req.Msg.Hidden
	if err := s.deps.Repo.SetVisibilityOverride(ctx, userID, int32(req.Msg.ItemType), req.Msg.Slug, isVisible); err != nil {
		logging.ErrorContext(ctx, "Failed to set visibility override", "error", err, "type", req.Msg.ItemType, "slug", req.Msg.Slug)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update hidden state"))
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

// ============================================================================
// Sub-phase 6c: Prompts & Active Settings
// ============================================================================

// GetPrompts retrieves global user prompts
func (s *Service) GetPrompts(ctx context.Context, req *connect.Request[reliantv1.GetPromptsRequest]) (*connect.Response[reliantv1.GetPromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	setting, err := s.deps.Repo.GetSetting(ctx, userID, nil, "global.prompts")
	if err != nil {
		// Return empty prompts if not found
		return connect.NewResponse(&reliantv1.GetPromptsResponse{
			Prompts: []*reliantv1.UserPrompt{},
		}), nil
	}

	var prompts []struct {
		ID       string  `json:"id"`
		Title    string  `json:"title"`
		Content  string  `json:"content"`
		Default  *bool   `json:"default,omitempty"`
		Hotkey   *string `json:"hotkey,omitempty"`
		Category *string `json:"category,omitempty"`
	}

	if setting.Value != "" {
		if err := json.Unmarshal([]byte(setting.Value), &prompts); err != nil {
			logging.ErrorContext(ctx, "Failed to unmarshal prompts", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse prompts"))
		}
	}

	protoPrompts := make([]*reliantv1.UserPrompt, len(prompts))
	for i, p := range prompts {
		protoPrompts[i] = &reliantv1.UserPrompt{
			Id:       p.ID,
			Title:    p.Title,
			Content:  p.Content,
			Default:  p.Default,
			Hotkey:   p.Hotkey,
			Category: p.Category,
		}
	}

	return connect.NewResponse(&reliantv1.GetPromptsResponse{
		Prompts: protoPrompts,
	}), nil
}

// SavePrompts saves global user prompts
func (s *Service) SavePrompts(ctx context.Context, req *connect.Request[reliantv1.SavePromptsRequest]) (*connect.Response[reliantv1.SavePromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Convert proto prompts to JSON-serializable struct
	prompts := make([]map[string]interface{}, len(req.Msg.Prompts))
	for i, p := range req.Msg.Prompts {
		prompt := map[string]interface{}{
			"id":      p.Id,
			"title":   p.Title,
			"content": p.Content,
		}
		if p.Default != nil {
			prompt["default"] = *p.Default
		}
		if p.Hotkey != nil {
			prompt["hotkey"] = *p.Hotkey
		}
		if p.Category != nil {
			prompt["category"] = *p.Category
		}
		prompts[i] = prompt
	}

	promptsJSON, err := json.Marshal(prompts)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to serialize prompts"))
	}

	if err := s.upsertSetting(ctx, userID, nil, "global.prompts", string(promptsJSON)); err != nil {
		logging.ErrorContext(ctx, "Failed to save prompts", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save prompts"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after SavePrompts", "error", err)
	}

	return connect.NewResponse(&reliantv1.SavePromptsResponse{
		Success: true,
		Message: "Prompts saved successfully",
		Prompts: req.Msg.Prompts,
	}), nil
}

// ============================================================================
// Sub-phase 6d: Provider Settings (API Keys)
// ============================================================================

// providerOutputModalities returns the distinct output modalities the given
// provider's models can generate, derived from the model registry.
//
// Derived rather than hardcoded per provider: adding an image model to
// models.yaml makes its provider image-capable with no change here. A static
// per-provider map would drift the moment a provider ships a new endpoint, and
// the drift would be silent — the UI would keep claiming a capability the
// registry no longer backs, or hide one it does.
//
// Order is stable (text first, then the generative extras alphabetically) so
// the UI and any test see a deterministic list rather than map order.
func providerOutputModalities(provider string) []string {
	registry, err := models.GetRegistry()
	if err != nil {
		logging.Warn("Failed to load model registry for provider modalities", "error", err)
		return nil
	}

	seen := make(map[models.Modality]bool)
	for _, def := range registry.ListModelsByProvider(provider) {
		for _, m := range def.Capabilities.EffectiveOutputModalities() {
			seen[m] = true
		}
	}

	result := make([]string, 0, len(seen))
	if seen[models.ModalityText] {
		result = append(result, string(models.ModalityText))
	}
	extras := make([]string, 0, len(seen))
	for m := range seen {
		if m != models.ModalityText {
			extras = append(extras, string(m))
		}
	}
	sort.Strings(extras)
	return append(result, extras...)
}

// providerDisplayNames name the providers GetProviderStatuses reports, in display order.
var statusProviders = []struct{ id, name string }{
	{"claude", "Claude subscription"},
	{"codex", "Codex (ChatGPT)"},
	{"copilot", "GitHub Copilot"},
	{antigravity.DriverID, "Antigravity"},
	{"openrouter", "OpenRouter"},
	{"anthropic", "Anthropic API"},
	{"openai", "OpenAI"},
	{"gemini", "Google Gemini"},
	{"reliant", "Reliant"},
}

// GetProviderStatuses reports which providers can serve, derived from the user's accounts: a
// provider is configured when at least one of its accounts is not waiting for sign-in. Masked
// keys and per-account detail live on the accounts (ProviderAccountService).
func (s *Service) GetProviderStatuses(ctx context.Context, req *connect.Request[reliantv1.GetProviderStatusesRequest]) (*connect.Response[reliantv1.GetProviderStatusesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	accounts, err := s.deps.Repo.ListProviderAccounts(ctx, userID)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to list provider accounts", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get provider statuses"))
	}
	total := map[string]int32{}
	usable := map[string]bool{}
	for _, a := range accounts {
		total[a.Provider]++
		if a.NeedsReauthAt == nil {
			usable[a.Provider] = true
		}
	}

	statuses := make([]*reliantv1.ProviderStatus, 0, len(statusProviders))
	for _, p := range statusProviders {
		statuses = append(statuses, &reliantv1.ProviderStatus{
			Provider:         p.id,
			DisplayName:      p.name,
			Configured:       usable[p.id],
			AccountCount:     total[p.id],
			OutputModalities: providerOutputModalities(p.id),
		})
	}
	return connect.NewResponse(&reliantv1.GetProviderStatusesResponse{Providers: statuses}), nil
}

// ValidateProviderAPIKey validates a pasted API key for a provider. Subscription providers have
// no key to validate: they answer whether the user has a working account.
func (s *Service) ValidateProviderAPIKey(ctx context.Context, req *connect.Request[reliantv1.ValidateProviderAPIKeyRequest]) (*connect.Response[reliantv1.ValidateProviderAPIKeyResponse], error) {
	provider := models.Family(req.Msg.Provider)
	userID := auth.MustGetUserID(ctx)

	switch provider {
	case "claude", "codex", "copilot", antigravity.DriverID:
		name := providerName(string(provider))
		accounts, err := s.deps.Repo.ListProviderAccounts(ctx, userID)
		if err != nil {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Failed to load " + name + " credentials"}), nil
		}
		have, usable := false, false
		for _, a := range accounts {
			if a.Provider == string(provider) {
				have = true
				usable = usable || a.NeedsReauthAt == nil
			}
		}
		switch {
		case !have:
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: name + " is not connected"}), nil
		case !usable:
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: name + " session expired. Please reconnect " + name + "."}), nil
		}
		return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: true, Message: "Connected to " + name}), nil
	}

	valid, message := s.deps.ValidateKey(ctx, provider, req.Msg.ApiKey)
	return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: valid, Message: message}), nil
}

func requireOAuthParams(code, codeVerifier, redirectURI string) error {
	switch {
	case code == "":
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("authorization code is required"))
	case codeVerifier == "":
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("code_verifier is required"))
	case redirectURI == "":
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("redirect_uri is required"))
	}
	return nil
}

// connectedMessage is the success line for a sign-in, naming what happened to the account.
func connectedMessage(provider string, res signInResult) string {
	name := providerName(provider)
	if res.created {
		return "Connected to " + name
	}
	return name + " account already connected; signed in again"
}

// CompleteCodexOAuth exchanges an OAuth authorization code + PKCE verifier and stores the result
// as a Codex account: labelled by the ChatGPT email, planned from the id_token, deduped by the
// ChatGPT account and user.
func (s *Service) CompleteCodexOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteCodexOAuthRequest]) (*connect.Response[reliantv1.CompleteCodexOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)
	if err := requireOAuthParams(code, codeVerifier, redirectURI); err != nil {
		return nil, err
	}

	tokens, err := s.deps.ExchangeCodexCode(code, codeVerifier, redirectURI)
	if err != nil {
		logging.ErrorContext(ctx, "Codex OAuth code exchange failed", "error", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to complete Codex OAuth: %w", err))
	}

	res, err := s.connectAccount(ctx, signIn{
		userID: userID, provider: "codex", targetID: req.Msg.GetAccountId(),
		upsert: codexIdentity(tokens.AccessToken, tokens.RefreshToken, tokens.IDToken, tokens.AccountID),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.CompleteCodexOAuthResponse{
		Success: true, Message: connectedMessage("codex", res),
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}), nil
}

// CompleteAntigravityOAuth exchanges a Google OAuth authorization code + PKCE verifier for
// Antigravity tokens and stores them as an account keyed by the Google subject.
//
// Unlike CompleteClaudeOAuth there is no state parameter: Google's token
// endpoint does not require it echoed back. The frontend still generates and
// verifies state — it just never reaches the backend.
func (s *Service) CompleteAntigravityOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteAntigravityOAuthRequest]) (*connect.Response[reliantv1.CompleteAntigravityOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)
	if err := requireOAuthParams(code, codeVerifier, redirectURI); err != nil {
		return nil, err
	}

	tokens, err := s.deps.ExchangeAntigravityCode(code, codeVerifier, redirectURI)
	if err != nil {
		logging.ErrorContext(ctx, "Antigravity OAuth code exchange failed", "error", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to complete Antigravity OAuth: %w", err))
	}

	u := core.AccountUpsert{
		AuthKind: core.AuthKindOAuth, Scope: tokens.Scope, ExpiresAt: tokens.ExpiresAt,
		Secrets: core.AccountSecrets{Secret: tokens.AccessToken, Refresh: tokens.RefreshToken, IDToken: tokens.IDToken},
	}
	if id, ok := core.GoogleIdentityFromIDToken(tokens.IDToken); ok {
		u.IdentityKey, u.Email, u.ExternalAccountID = id.IdentityKey, id.Email, id.ExternalAccountID
	}
	res, err := s.connectAccount(ctx, signIn{userID: userID, provider: antigravity.DriverID, targetID: req.Msg.GetAccountId(), upsert: u})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.CompleteAntigravityOAuthResponse{
		Success: true, Message: connectedMessage(antigravity.DriverID, res),
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}), nil
}

// StartCopilotDeviceAuth begins the GitHub Copilot device-authorization flow and
// returns the user code + verification URI for the client to present.
func (s *Service) StartCopilotDeviceAuth(ctx context.Context, req *connect.Request[reliantv1.StartCopilotDeviceAuthRequest]) (*connect.Response[reliantv1.StartCopilotDeviceAuthResponse], error) {
	// userID is not required to start the flow, but require an authenticated
	// caller for parity with the other provider handlers.
	_ = auth.MustGetUserID(ctx)

	deviceAuth, err := copilot.StartDeviceAuth(ctx)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to start Copilot device authorization", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start GitHub Copilot login"))
	}

	return connect.NewResponse(&reliantv1.StartCopilotDeviceAuthResponse{
		DeviceCode:       deviceAuth.DeviceCode,
		UserCode:         deviceAuth.UserCode,
		VerificationUri:  deviceAuth.VerificationURI,
		IntervalSeconds:  int32(deviceAuth.Interval),
		ExpiresInSeconds: int32(deviceAuth.ExpiresIn),
	}), nil
}

// PollCopilotDeviceAuth performs a single poll of the GitHub device flow. On success it stores
// the GitHub token as a Copilot account keyed by the GitHub user id.
func (s *Service) PollCopilotDeviceAuth(ctx context.Context, req *connect.Request[reliantv1.PollCopilotDeviceAuthRequest]) (*connect.Response[reliantv1.PollCopilotDeviceAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)

	deviceCode := strings.TrimSpace(req.Msg.DeviceCode)
	if deviceCode == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("device_code is required"))
	}

	tokens, err := s.deps.PollCopilotDevice(ctx, deviceCode)
	if err != nil {
		switch {
		case errors.Is(err, copilot.ErrAuthorizationPending), errors.Is(err, copilot.ErrSlowDown):
			return connect.NewResponse(&reliantv1.PollCopilotDeviceAuthResponse{
				Status: reliantv1.PollCopilotDeviceAuthResponse_STATUS_PENDING,
			}), nil
		case errors.Is(err, copilot.ErrDeviceCodeExpired):
			return connect.NewResponse(&reliantv1.PollCopilotDeviceAuthResponse{
				Status: reliantv1.PollCopilotDeviceAuthResponse_STATUS_EXPIRED,
			}), nil
		case errors.Is(err, copilot.ErrAccessDenied):
			return connect.NewResponse(&reliantv1.PollCopilotDeviceAuthResponse{
				Status: reliantv1.PollCopilotDeviceAuthResponse_STATUS_DENIED,
			}), nil
		default:
			logging.ErrorContext(ctx, "Copilot device authorization poll failed", "error", err)
			return connect.NewResponse(&reliantv1.PollCopilotDeviceAuthResponse{
				Status:       reliantv1.PollCopilotDeviceAuthResponse_STATUS_ERROR,
				ErrorMessage: err.Error(),
			}), nil
		}
	}

	// Individual is the only supported GitHub Copilot tier. Ignore whatever tier the client sent
	// (including a legacy "enterprise") so the driver routes to api.individual.githubcopilot.com.
	u := core.AccountUpsert{
		AuthKind: core.AuthKindDevice, PlanRaw: "individual", Scope: tokens.Scope,
		Secrets: core.AccountSecrets{Secret: tokens.AccessToken},
	}
	var label string
	if s.deps.GitHubIdentity != nil {
		gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		gh, ghErr := s.deps.GitHubIdentity(gctx, tokens.AccessToken)
		cancel()
		if ghErr != nil {
			logging.WarnContext(ctx, "GitHub identity read failed; the Copilot account stays unlabelled", "error", ghErr)
		} else if gh.ID != 0 {
			u.IdentityKey = core.GitHubIdentityKey(strconv.FormatInt(gh.ID, 10))
			u.ExternalAccountID, u.Email, label = strconv.FormatInt(gh.ID, 10), gh.Email, gh.Login
		}
	}
	res, err := s.connectAccount(ctx, signIn{userID: userID, provider: "copilot", targetID: req.Msg.GetAccountId(), upsert: u, label: label})
	if err != nil {
		return nil, err
	}

	// Drop any cached per-account model availability so the picker re-queries GET
	// /models for the freshly-connected account on its next load.
	copilot.EvictAvailabilityCache(tokens.AccessToken)

	return connect.NewResponse(&reliantv1.PollCopilotDeviceAuthResponse{
		Status:  reliantv1.PollCopilotDeviceAuthResponse_STATUS_AUTHORIZED,
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}), nil
}

// CompleteClaudeOAuth exchanges an OAuth authorization code + PKCE verifier and stores the result
// as a Claude subscription account: labelled by the account email, planned from the profile
// endpoint, deduped by account and organization.
func (s *Service) CompleteClaudeOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteClaudeOAuthRequest]) (*connect.Response[reliantv1.CompleteClaudeOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)
	state := strings.TrimSpace(req.Msg.State)
	if err := requireOAuthParams(code, codeVerifier, redirectURI); err != nil {
		return nil, err
	}

	tokens, err := s.deps.ExchangeClaudeCode(code, codeVerifier, redirectURI, state)
	if err != nil {
		logging.ErrorContext(ctx, "Claude OAuth code exchange failed", "error", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to complete Claude OAuth: %w", err))
	}

	res, err := s.connectAccount(ctx, signIn{
		userID: userID, provider: "claude", targetID: req.Msg.GetAccountId(),
		upsert: s.claudeIdentity(ctx, tokens.AccessToken, tokens.AccountUUID, tokens.OrganizationUUID,
			tokens.AccountEmail, tokens.OrganizationName, tokens.Scope, tokens.ExpiresAt, tokens.RefreshToken),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.CompleteClaudeOAuthResponse{
		Success: true, Message: connectedMessage("claude", res),
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}), nil
}

// SyncReliantProvider mints the user's LLM gateway key via control-plane and
// persists it locally as the "reliant" provider credential.
//
// The key is an `rlat_` access token (llm:invoke, acting as the user) for the
// device controlplane.ReliantProviderKeyName. Control-plane returns a NEW
// plaintext on every call and revokes this device's previous key in the same
// transaction — it never re-serves a stored plaintext — so created/rotated
// come from whether a previous key was replaced, not from comparing strings.
func (s *Service) SyncReliantProvider(ctx context.Context, req *connect.Request[reliantv1.SyncReliantProviderRequest]) (*connect.Response[reliantv1.SyncReliantProviderResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if s.deps.ControlPlane == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("control-plane client not configured"))
	}

	jwt, ok := auth.GetUserJWT(userID)
	if !ok || strings.TrimSpace(jwt) == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("missing user JWT for control-plane call"))
	}

	key, err := s.deps.ControlPlane.MintLLMKey(ctx, jwt, controlplane.ReliantProviderKeyName)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to mint Reliant LLM key via control-plane", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to mint Reliant LLM key: %w", err))
	}
	plaintext := strings.TrimSpace(key.Plaintext)
	if plaintext == "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("control-plane returned an empty Reliant LLM key"))
	}

	_, _, err = s.deps.Repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: userID, Provider: "reliant", AuthKind: core.AuthKindManaged, IdentityKey: core.ManagedIdentityKey,
		KeyHint: core.KeyHint(plaintext), Secrets: core.AccountSecrets{Secret: plaintext},
	})
	if err != nil {
		logging.ErrorContext(ctx, "Failed to persist Reliant provider API key", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to persist Reliant API key"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after SyncReliantProvider", "error", err)
	}

	created := !key.Rotated
	rotated := key.Rotated

	return connect.NewResponse(&reliantv1.SyncReliantProviderResponse{
		Success:    true,
		Message:    "Reliant provider synced",
		Synced:     true,
		CreatedKey: created,
		RotatedKey: rotated,
		Provider: &reliantv1.ProviderStatus{
			Provider:     "reliant",
			DisplayName:  "Reliant",
			Configured:   true,
			AccountCount: 1,
		},
	}), nil
}

// ============================================================================
// Sub-phase 6e: Privacy Settings
// ============================================================================

// GetPrivacySettings retrieves user privacy settings
func (s *Service) GetPrivacySettings(ctx context.Context, req *connect.Request[reliantv1.GetPrivacySettingsRequest]) (*connect.Response[reliantv1.GetPrivacySettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	analyticsEnabled, crashReportingEnabled, err := s.deps.Repo.GetPrivacySettings(ctx, userID)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to get privacy settings", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get privacy settings"))
	}

	return connect.NewResponse(&reliantv1.GetPrivacySettingsResponse{
		AnalyticsEnabled:      analyticsEnabled,
		CrashReportingEnabled: crashReportingEnabled,
	}), nil
}

// UpdatePrivacySettings updates user privacy settings
func (s *Service) UpdatePrivacySettings(ctx context.Context, req *connect.Request[reliantv1.UpdatePrivacySettingsRequest]) (*connect.Response[reliantv1.UpdatePrivacySettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get current settings
	analyticsEnabled, crashReportingEnabled, err := s.deps.Repo.GetPrivacySettings(ctx, userID)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to get current privacy settings", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update privacy settings"))
	}

	// Update only the fields that were provided
	if req.Msg.AnalyticsEnabled != nil {
		analyticsEnabled = *req.Msg.AnalyticsEnabled
	}
	if req.Msg.CrashReportingEnabled != nil {
		crashReportingEnabled = *req.Msg.CrashReportingEnabled
	}

	// Save the updated settings
	if err := s.deps.Repo.SetPrivacySettings(ctx, userID, analyticsEnabled, crashReportingEnabled); err != nil {
		logging.ErrorContext(ctx, "Failed to save privacy settings", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save privacy settings"))
	}

	logging.InfoContext(ctx, "Privacy settings updated", "userID", userID, "analyticsEnabled", analyticsEnabled, "crashReportingEnabled", crashReportingEnabled)

	return connect.NewResponse(&reliantv1.UpdatePrivacySettingsResponse{
		Success:               true,
		Message:               "Privacy settings updated successfully",
		AnalyticsEnabled:      analyticsEnabled,
		CrashReportingEnabled: crashReportingEnabled,
		RequiresRestart:       false,
	}), nil
}

// TrackPageVisited tracks a page visit for analytics
func (s *Service) TrackPageVisited(ctx context.Context, req *connect.Request[reliantv1.TrackPageVisitedRequest]) (*connect.Response[reliantv1.TrackPageVisitedResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get analytics client that respects user's privacy settings
	analyticsClient := analytics.ForUser(ctx, userID)

	metrics := analytics.PageVisitedMetrics{
		PageName: req.Msg.PageName,
	}
	if req.Msg.PreviousPage != nil {
		metrics.PreviousPage = *req.Msg.PreviousPage
	}

	analyticsClient.TrackPageVisited(metrics)

	return connect.NewResponse(&reliantv1.TrackPageVisitedResponse{
		Success: true,
	}), nil
}

// TrackOnboardingEvent tracks an onboarding flow event for analytics.
// TODO: Uncomment after running `buf generate` to regenerate proto types.
//
// func (s *Service) TrackOnboardingEvent(ctx context.Context, req *connect.Request[reliantv1.TrackOnboardingEventRequest]) (*connect.Response[reliantv1.TrackOnboardingEventResponse], error) {
// 	userID := auth.MustGetUserID(ctx)
// 	analyticsClient := analytics.ForUser(ctx, userID)
//
// 	eventType := analytics.EventType(req.Msg.EventType)
// 	// Validate event type
// 	validTypes := map[analytics.EventType]bool{
// 		analytics.EventOnboardingStarted:       true,
// 		analytics.EventOnboardingStepCompleted: true,
// 		analytics.EventOnboardingStepSkipped:   true,
// 		analytics.EventOnboardingCompleted:     true,
// 		analytics.EventOnboardingSkipped:       true,
// 	}
// 	if !validTypes[eventType] {
// 		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid onboarding event type: %s", req.Msg.EventType))
// 	}
//
// 	metrics := analytics.OnboardingMetrics{
// 		TotalSteps:     int(req.Msg.TotalSteps),
// 		StepsCompleted: int(req.Msg.StepsCompleted),
// 		StepsSkipped:   int(req.Msg.StepsSkipped),
// 	}
// 	if req.Msg.StepId != nil {
// 		metrics.StepID = *req.Msg.StepId
// 	}
// 	if req.Msg.StepName != nil {
// 		metrics.StepName = *req.Msg.StepName
// 	}
//
// 	analyticsClient.TrackOnboardingEvent(eventType, metrics)
//
// 	return connect.NewResponse(&reliantv1.TrackOnboardingEventResponse{
// 		Success: true,
// 	}), nil
// }

// ============================================================================
// Configuration Health
// ============================================================================

// GetConfigHealth retrieves configuration errors and warnings.
// Collects errors from preset, workflow, and MCP loading.
func (s *Service) GetConfigHealth(
	ctx context.Context,
	req *connect.Request[reliantv1.GetConfigHealthRequest],
) (*connect.Response[reliantv1.GetConfigHealthResponse], error) {
	collector := validation.Global()

	// Get all errors from the collector
	errors := collector.Errors()

	// Credential health is per-user, so it is computed here rather than held in
	// the process-global collector.
	if userID, ok := auth.GetUserIDFromContext(ctx); ok {
		if stale, err := drivers.HasStaleReliantKey(ctx, s.deps.Repo, userID); err == nil && stale {
			// Heal first: the message is only true if the replacement failed.
			if _, configured, healErr := drivers.HealReliantKey(ctx, userID); healErr != nil {
				errors = append(errors, validation.NewError(validation.CategoryConfig,
					"Your Reliant LLM key is in a legacy format the gateway rejects, so Reliant models are unavailable, and automatic replacement failed: "+healErr.Error()+". Reconnect your Reliant account (re-sync) to replace the key.").
					Source("provider:reliant").
					Build())
			} else if !configured {
				errors = append(errors, validation.NewError(validation.CategoryConfig,
					"Your Reliant LLM key is in a legacy format the gateway rejects, so Reliant models are unavailable, and this deployment cannot replace it automatically. Reconnect your Reliant account (re-sync) to replace the key.").
					Source("provider:reliant").
					Build())
			}
		}
	}

	// Apply type filter if specified
	if req.Msg.TypeFilter != nil && *req.Msg.TypeFilter != "" {
		filtered := make([]*validation.Error, 0)
		for _, err := range errors {
			if string(err.Category) == *req.Msg.TypeFilter {
				filtered = append(filtered, err)
			}
		}
		errors = filtered
	}

	// Convert to proto
	protoErrors := make([]*reliantv1.ConfigError, 0, len(errors))
	var errorCount, warningCount int32

	for _, err := range errors {
		protoErr := &reliantv1.ConfigError{
			Type:     string(err.Category),
			Source:   err.Source,
			Message:  err.Message,
			Severity: configSeverityFromString(string(err.Severity)),
		}

		// Convert details map
		if err.Details != nil {
			protoErr.Details = make(map[string]string)
			for k, v := range err.Details {
				if strVal, ok := v.(string); ok {
					protoErr.Details[k] = strVal
				} else {
					// Convert non-string values to JSON
					if jsonBytes, jsonErr := json.Marshal(v); jsonErr == nil {
						protoErr.Details[k] = string(jsonBytes)
					}
				}
			}
		}

		protoErrors = append(protoErrors, protoErr)

		if err.Severity == validation.SeverityError {
			errorCount++
		} else {
			warningCount++
		}
	}

	return connect.NewResponse(&reliantv1.GetConfigHealthResponse{
		Errors:       protoErrors,
		ErrorCount:   errorCount,
		WarningCount: warningCount,
	}), nil
}
