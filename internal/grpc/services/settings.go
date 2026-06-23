// Copyright (c) 2025 Reliant Labs
package services

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

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/models"

	"github.com/reliant-labs/reliant/internal/logging"

	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/validation"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// SettingsService implements the SettingsService RPC handlers
type SettingsService struct {
	reliantv1connect.UnimplementedSettingsServiceHandler
	database           db.Repository
	daemonRouter       toolexec.DaemonRouter
	controlPlaneClient controlplane.Client
}

// NewSettingsService creates a new SettingsService
func NewSettingsService(database db.Repository, daemonRouter toolexec.DaemonRouter) *SettingsService {
	return &SettingsService{
		database:           database,
		daemonRouter:       daemonRouter,
		controlPlaneClient: controlplane.NewClient(""),
	}
}

// WithControlPlaneClient overrides the default control-plane client. Intended for tests.
func (s *SettingsService) WithControlPlaneClient(client controlplane.Client) *SettingsService {
	s.controlPlaneClient = client
	return s
}

// sendDaemonCommand sends a command to the user's daemon and unmarshals the response.
func (s *SettingsService) sendDaemonCommand(ctx context.Context, userID, commandType string, payload interface{}, resp interface{}) error {
	if s.daemonRouter == nil {
		return fmt.Errorf("daemon router not available")
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	respBytes, err := s.daemonRouter.SendDaemonCommand(ctx, userID, commandType, payloadBytes, 30_000)
	if err != nil {
		return fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	if resp != nil {
		if err := json.Unmarshal(respBytes, resp); err != nil {
			return fmt.Errorf("unmarshal response for %s: %w", commandType, err)
		}
	}
	return nil
}

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
func (s *SettingsService) upsertSetting(ctx context.Context, userID string, projectID *string, key, value string) error {
	setting, err := s.database.GetSetting(ctx, userID, projectID, key)
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
		return s.database.CreateSetting(ctx, newSetting)
	}

	// Setting exists, update it
	setting.Value = value
	setting.UpdatedAt = time.Now().UTC()
	return s.database.UpdateSetting(ctx, setting)
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

// validateAPIKey validates an API key for a provider
func (s *SettingsService) validateAPIKey(ctx context.Context, provider models.Family, apiKey string) (bool, string) {
	registry := models.MustGetRegistry()
	providerDefs := registry.ListModelsByProvider(string(provider))
	if len(providerDefs) == 0 {
		return false, fmt.Sprintf("No models found for provider: %s", provider)
	}
	testModel := providerDefs[0].ToModel()

	resolvedAPIKey := apiKey
	driverOpts := []llm.DriverOption{
		llm.WithMaxTokens(100),
	}
	if provider == "reliant" {
		baseURL := drivers.ResolveReliantBaseURL(apiKey)
		var extraHeaders map[string]string
		resolvedAPIKey, extraHeaders = drivers.ResolveReliantAPIKey(apiKey, baseURL)
		driverOpts = append(driverOpts, llm.WithBaseURL(baseURL))
		if len(extraHeaders) > 0 {
			driverOpts = append(driverOpts, llm.WithExtraHeaders(extraHeaders))
		}
	}
	driverOpts = append(driverOpts, llm.WithAPIKey(resolvedAPIKey))
	driver, err := drivers.GetDriverForModel(testModel, provider, driverOpts...)
	if err != nil {
		logging.Error("Failed to create driver for validation", "provider", provider, "error", err)
		return false, fmt.Sprintf("Failed to initialize provider: %v", err)
	}

	err = driver.ValidateKey(ctx)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "unauthorized") ||
			strings.Contains(errStr, "authentication") ||
			strings.Contains(errStr, "invalid") ||
			strings.Contains(errStr, "api key") ||
			strings.Contains(errStr, "401") {
			return false, "Invalid API key"
		}
		logging.Warn("API key validation error (may not be auth-related)", "provider", provider, "error", err)
		return false, fmt.Sprintf("Validation failed: %v", err)
	}

	return true, "API key is valid"
}

func parsePageSize(value int32, fallback, max int) int {
	if value <= 0 {
		return fallback
	}
	if int(value) > max {
		return max
	}
	return int(value)
}

func parsePageOffset(token string, total int) (int, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid page_token")
	}
	if offset < 0 || offset > total {
		return 0, fmt.Errorf("invalid page_token")
	}
	return offset, nil
}

func buildNextPageToken(nextOffset, total int) string {
	if nextOffset >= total {
		return ""
	}
	return strconv.Itoa(nextOffset)
}

// ============================================================================
// Sub-phase 6a: Core Settings CRUD
// ============================================================================

// CreateSetting creates a new setting
func (s *SettingsService) CreateSetting(ctx context.Context, req *connect.Request[reliantv1.CreateSettingRequest]) (*connect.Response[reliantv1.CreateSettingResponse], error) {
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

	if err := s.database.CreateSetting(ctx, setting); err != nil {
		logging.Error("Failed to create setting", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create setting"))
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.Warn("Failed to emit config_health refetch after CreateSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.CreateSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}

// ListSettings lists all settings for the current user
func (s *SettingsService) ListSettings(ctx context.Context, req *connect.Request[reliantv1.ListSettingsRequest]) (*connect.Response[reliantv1.ListSettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	settings, err := s.database.ListSettings(ctx, userID, projectID)
	if err != nil {
		logging.Error("Failed to list settings", "error", err)
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
func (s *SettingsService) GetSetting(ctx context.Context, req *connect.Request[reliantv1.GetSettingRequest]) (*connect.Response[reliantv1.GetSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.database.GetSetting(ctx, userID, projectID, req.Msg.Key)
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
func (s *SettingsService) UpdateSetting(ctx context.Context, req *connect.Request[reliantv1.UpdateSettingRequest]) (*connect.Response[reliantv1.UpdateSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.database.GetSetting(ctx, userID, projectID, req.Msg.Key)
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

	if err := s.database.UpdateSetting(ctx, setting); err != nil {
		logging.Error("Failed to update setting", "error", err, "key", req.Msg.Key)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update setting"))
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.Warn("Failed to emit config_health refetch after UpdateSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.UpdateSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}

// DeleteSetting deletes a setting by key
func (s *SettingsService) DeleteSetting(ctx context.Context, req *connect.Request[reliantv1.DeleteSettingRequest]) (*connect.Response[reliantv1.DeleteSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.database.GetSetting(ctx, userID, projectID, req.Msg.Key)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("setting not found"))
	}

	if err := s.database.DeleteSetting(ctx, setting.ID); err != nil {
		logging.Error("Failed to delete setting", "error", err, "key", req.Msg.Key)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete setting"))
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.Warn("Failed to emit config_health refetch after DeleteSetting", "error", err)
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
func (s *SettingsService) GetShortcuts(ctx context.Context, req *connect.Request[reliantv1.GetShortcutsRequest]) (*connect.Response[reliantv1.GetShortcutsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	setting, err := s.database.GetSetting(ctx, userID, nil, "shortcuts")
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
func (s *SettingsService) UpdateShortcuts(ctx context.Context, req *connect.Request[reliantv1.UpdateShortcutsRequest]) (*connect.Response[reliantv1.UpdateShortcutsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.upsertSetting(ctx, userID, nil, "shortcuts", req.Msg.Shortcuts); err != nil {
		logging.Error("Failed to update shortcuts", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update shortcuts"))
	}

	return connect.NewResponse(&reliantv1.UpdateShortcutsResponse{
		Success: true,
		Message: "Shortcuts updated successfully",
	}), nil
}

// GetPreferences retrieves user preferences
func (s *SettingsService) GetPreferences(ctx context.Context, req *connect.Request[reliantv1.GetPreferencesRequest]) (*connect.Response[reliantv1.GetPreferencesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get streaming enabled
	streamingEnabled := true
	if setting, err := s.database.GetSetting(ctx, userID, nil, "features.streaming_enabled"); err == nil {
		streamingEnabled = setting.Value == "true"
	}

	// Worktree archive mode
	worktreeArchiveMode := "ask_me"
	if setting, err := s.database.GetSetting(ctx, userID, nil, "worktree.archive_cleanup_mode"); err == nil {
		worktreeArchiveMode = setting.Value
	}

	// Worktree delete directory default
	worktreeDeleteDir := true
	if setting, err := s.database.GetSetting(ctx, userID, nil, "worktree.default_delete_directory"); err == nil {
		worktreeDeleteDir = setting.Value == "true"
	}

	// Worktree delete branch default
	worktreeDeleteBranch := false
	if setting, err := s.database.GetSetting(ctx, userID, nil, "worktree.default_delete_branch"); err == nil {
		worktreeDeleteBranch = setting.Value == "true"
	}

	// Branch copy uncommitted files default (defaults to false if not set)
	branchCopyUncommittedFilesDefault := false
	if setting, err := s.database.GetSetting(ctx, userID, nil, "worktree.branch_copy_uncommitted_files_default"); err == nil {
		branchCopyUncommittedFilesDefault = setting.Value == "true"
	}

	// Default MCP scope (defaults to PROJECT if not set)
	defaultMcpScope := reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT
	if setting, err := s.database.GetSetting(ctx, userID, nil, "config.default_mcp_scope"); err == nil {
		if scope, ok := reliantv1.ConfigScope_value[setting.Value]; ok {
			defaultMcpScope = reliantv1.ConfigScope(scope)
		}
	}

	// Default workflow scope (defaults to PROJECT if not set)
	defaultWorkflowScope := reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT
	if setting, err := s.database.GetSetting(ctx, userID, nil, "config.default_workflow_scope"); err == nil {
		if scope, ok := reliantv1.ConfigScope_value[setting.Value]; ok {
			defaultWorkflowScope = reliantv1.ConfigScope(scope)
		}
	}

	// Default workflow (defaults to builtin://agent if not set)
	defaultWorkflow := workflow.DefaultWorkflow
	if setting, err := s.database.GetSetting(ctx, userID, nil, "config.default_workflow"); err == nil && setting.Value != "" {
		defaultWorkflow = setting.Value
	}

	// Hide builtin workflows (defaults to false)
	hideBuiltinWorkflows := false
	if setting, err := s.database.GetSetting(ctx, userID, nil, "ui.hide_builtin_workflows"); err == nil {
		hideBuiltinWorkflows = setting.Value == "true"
	}

	// Hide builtin presets (defaults to false)
	hideBuiltinPresets := false
	if setting, err := s.database.GetSetting(ctx, userID, nil, "ui.hide_builtin_presets"); err == nil {
		hideBuiltinPresets = setting.Value == "true"
	}

	// Collect additional arbitrary preferences
	// Filter out keys that have dedicated fields to avoid duplicates
	excludedPrefKeys := map[string]bool{
		"default_planning_mode": true, // Has dedicated field
		"default_auto_approve":  true, // Has dedicated field
	}
	additional := make(map[string]string)
	allSettings, err := s.database.ListSettings(ctx, userID, nil)
	if err == nil {
		for _, setting := range allSettings {
			if len(setting.Key) > 11 && setting.Key[:11] == "preference." {
				key := setting.Key[11:] // Remove "preference." prefix
				if !excludedPrefKeys[key] {
					additional[key] = setting.Value
				}
			}
		}
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
		WorktreeDefaultDeleteDirectory:    worktreeDeleteDir,
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
func (s *SettingsService) UpdatePreferences(ctx context.Context, req *connect.Request[reliantv1.UpdatePreferencesRequest]) (*connect.Response[reliantv1.UpdatePreferencesResponse], error) {
	userID := auth.MustGetUserID(ctx)
	analyticsClient := analytics.GetClientForUser(ctx, userID)
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
			logging.Error("Failed to update streaming_enabled", "error", err)
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
			logging.Error("Failed to update worktree.archive_cleanup_mode", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("worktree.archive_cleanup_mode")
	}

	if req.Msg.WorktreeDefaultDeleteDirectory != nil {
		value := "false"
		if *req.Msg.WorktreeDefaultDeleteDirectory {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.default_delete_directory", value); err != nil {
			logging.Error("Failed to update worktree.default_delete_directory", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("worktree.default_delete_directory")
	}

	if req.Msg.WorktreeDefaultDeleteBranch != nil {
		value := "false"
		if *req.Msg.WorktreeDefaultDeleteBranch {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.default_delete_branch", value); err != nil {
			logging.Error("Failed to update worktree.default_delete_branch", "error", err)
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
			logging.Error("Failed to update chat.default_auto_approve", "error", err)
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
			logging.Error("Failed to update worktree.branch_copy_uncommitted_files_default", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("worktree.branch_copy_uncommitted_files_default")
	}

	// Handle additional arbitrary preferences
	for key, value := range req.Msg.Additional {
		prefKey := fmt.Sprintf("preference.%s", key)
		if err := s.upsertSetting(ctx, userID, nil, prefKey, value); err != nil {
			logging.Error("Failed to update preference", "key", key, "error", err)
			// Don't fail the whole request for one preference
			continue
		}
		markChanged(prefKey)
	}

	// Handle default MCP scope
	if req.Msg.DefaultMcpScope != nil {
		scopeValue := req.Msg.DefaultMcpScope.String()
		if err := s.upsertSetting(ctx, userID, nil, "config.default_mcp_scope", scopeValue); err != nil {
			logging.Error("Failed to update config.default_mcp_scope", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("config.default_mcp_scope")
	}

	// Handle default workflow scope
	if req.Msg.DefaultWorkflowScope != nil {
		scopeValue := req.Msg.DefaultWorkflowScope.String()
		if err := s.upsertSetting(ctx, userID, nil, "config.default_workflow_scope", scopeValue); err != nil {
			logging.Error("Failed to update config.default_workflow_scope", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("config.default_workflow_scope")
	}

	// Handle default workflow
	if req.Msg.DefaultWorkflow != nil {
		if err := s.upsertSetting(ctx, userID, nil, "config.default_workflow", *req.Msg.DefaultWorkflow); err != nil {
			logging.Error("Failed to update config.default_workflow", "error", err)
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
			logging.Error("Failed to update ui.hide_builtin_workflows", "error", err)
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
			logging.Error("Failed to update ui.hide_builtin_presets", "error", err)
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

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after UpdatePreferences", "error", err)
	}

	return connect.NewResponse(&reliantv1.UpdatePreferencesResponse{
		Success: true,
		Message: "Preferences updated successfully",
	}), nil
}

// SetHiddenItem toggles the hidden state of a workflow or preset
func (s *SettingsService) SetHiddenItem(ctx context.Context, req *connect.Request[reliantv1.SetHiddenItemRequest]) (*connect.Response[reliantv1.SetHiddenItemResponse], error) {
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
	if err := s.database.SetVisibilityOverride(ctx, userID, int32(req.Msg.ItemType), req.Msg.Slug, isVisible); err != nil {
		logging.Error("Failed to set visibility override", "error", err, "type", req.Msg.ItemType, "slug", req.Msg.Slug)
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
func (s *SettingsService) getEffectiveHiddenItems(ctx context.Context, userID string, itemType int32) ([]string, error) {
	hiddenSet := make(map[string]bool)

	// Start with items hidden by default
	defaults, err := s.database.ListHiddenItemDefaults(ctx, itemType)
	if err != nil {
		return nil, fmt.Errorf("failed to list hidden item defaults: %w", err)
	}
	for _, slug := range defaults {
		hiddenSet[slug] = true
	}

	// Apply user overrides
	overrides, err := s.database.ListVisibilityOverrides(ctx, userID, itemType)
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
func (s *SettingsService) GetPrompts(ctx context.Context, req *connect.Request[reliantv1.GetPromptsRequest]) (*connect.Response[reliantv1.GetPromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	setting, err := s.database.GetSetting(ctx, userID, nil, "global.prompts")
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
			logging.Error("Failed to unmarshal prompts", "error", err)
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
func (s *SettingsService) SavePrompts(ctx context.Context, req *connect.Request[reliantv1.SavePromptsRequest]) (*connect.Response[reliantv1.SavePromptsResponse], error) {
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
		logging.Error("Failed to save prompts", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save prompts"))
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after SavePrompts", "error", err)
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

// GetProviderStatuses retrieves configuration status of all providers
func (s *SettingsService) GetProviderStatuses(ctx context.Context, req *connect.Request[reliantv1.GetProviderStatusesRequest]) (*connect.Response[reliantv1.GetProviderStatusesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get all API keys from dedicated table
	keys, err := s.database.GetProviderAPIKeys(ctx, userID)
	if err != nil {
		logging.Error("Failed to get provider API keys", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get provider statuses"))
	}

	// Mask the keys for display
	apiKeys := make(map[string]string)
	for provider, key := range keys {
		maskedKey := ""
		if len(key) > 8 {
			maskedKey = key[:4] + "..." + key[len(key)-4:]
		} else if len(key) > 0 {
			maskedKey = "***"
		}
		apiKeys[provider] = maskedKey
	}

	// Supported providers
	providers := []models.Family{
		"claude",
		"codex",
		"openrouter",
		"anthropic",
		"openai",
		"gemini",
	}

	providerDisplayNames := map[models.Family]string{
		"claude":     "Claude Code",
		"codex":      "Codex (ChatGPT)",
		"openrouter": "OpenRouter",
		"anthropic":  "Anthropic",
		"openai":     "OpenAI",
		"gemini":     "Google Gemini",
	}

	statuses := make([]*reliantv1.ProviderStatus, 0, len(providers))
	for _, provider := range providers {
		status := &reliantv1.ProviderStatus{
			Provider:    string(provider),
			DisplayName: providerDisplayNames[provider],
		}

		// Claude uses OAuth tokens persisted by Reliant.
		switch provider {
		case "claude":
			tokens, tokenErr := s.database.GetClaudeAuthTokens(ctx, userID)
			if tokenErr != nil {
				logging.Warn("Failed to load Claude auth tokens", "error", tokenErr)
			}

			configured := tokenErr == nil && tokens != nil && strings.TrimSpace(tokens.AccessToken) != ""
			if configured {
				if claude.IsTokenExpired(tokens.ExpiresAt) {
					configured = false
				}
			}

			status.Configured = configured
			status.HasApiKey = configured
			// Don't set MaskedKey for Claude - no API key to display
		case "codex":
			// Codex uses OAuth tokens persisted by Reliant.
			tokens, tokenErr := s.database.GetCodexAuthTokens(ctx, userID)
			if tokenErr != nil {
				logging.Warn("Failed to load Codex auth tokens", "error", tokenErr)
			}

			configured := tokenErr == nil && tokens != nil && strings.TrimSpace(tokens.AccessToken) != ""
			if configured {
				if codex.IsTokenExpired(tokens.AccessToken) {
					configured = false
				}
			}

			status.Configured = configured
			status.HasApiKey = configured
			// Don't set MaskedKey for Codex - no API key to display
		default:
			maskedKey, hasKey := apiKeys[string(provider)]
			status.Configured = hasKey
			status.HasApiKey = hasKey
			if hasKey {
				status.MaskedKey = &maskedKey
			}
		}

		statuses = append(statuses, status)
	}

	// Reliant provider is configured once the user has synced an rlnt_ key
	// from control-plane via SyncReliantProvider.
	reliantStatus := &reliantv1.ProviderStatus{
		Provider:    "reliant",
		DisplayName: "Reliant",
	}
	if maskedKey, hasKey := apiKeys["reliant"]; hasKey {
		reliantStatus.Configured = true
		reliantStatus.HasApiKey = true
		if maskedKey != "" {
			reliantStatus.MaskedKey = &maskedKey
		}
	}
	statuses = append(statuses, reliantStatus)

	return connect.NewResponse(&reliantv1.GetProviderStatusesResponse{
		Providers: statuses,
	}), nil
}

// UpdateProviderAPIKey updates or deletes an API key for a provider
func (s *SettingsService) UpdateProviderAPIKey(ctx context.Context, req *connect.Request[reliantv1.UpdateProviderAPIKeyRequest]) (*connect.Response[reliantv1.UpdateProviderAPIKeyResponse], error) {
	userID := auth.MustGetUserID(ctx)
	provider := req.Msg.Provider
	analyticsClient := analytics.GetClientForUser(ctx, userID)
	trackProviderEvent := func(action, authMethod string) {
		analyticsClient.TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{
			Provider:   provider,
			Action:     action,
			AuthMethod: authMethod,
		})

		// Also fire api_key_configured for connect/update actions
		if action == "connected" || action == "updated" {
			existingKeys, err := s.database.GetProviderAPIKeys(ctx, userID)
			totalProviders := 0
			if err == nil {
				for _, v := range existingKeys {
					if v != "" {
						totalProviders++
					}
				}
			}
			analyticsClient.TrackAPIKeyConfigured(analytics.APIKeyConfiguredMetrics{
				Provider:       provider,
				AuthMethod:     authMethod,
				IsFirstKey:     totalProviders <= 1,
				TotalProviders: totalProviders,
			})
		}
	}

	// Only supported providers
	validProviders := map[string]bool{
		"claude":     true,
		"codex":      true,
		"reliant":    true,
		"anthropic":  true,
		"openai":     true,
		"gemini":     true,
		"openrouter": true,
	}

	if !validProviders[provider] {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported provider: %s", provider))
	}

	if provider == "claude" {
		if strings.TrimSpace(req.Msg.ApiKey) != "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("claude does not use manual API keys; use Login with Claude in Settings"))
		}

		if err := s.database.DeleteClaudeAuthTokens(ctx, userID); err != nil {
			logging.Error("Failed to delete Claude auth tokens", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to disconnect from Claude"))
		}

		if err := s.database.DeleteProviderAPIKey(ctx, userID, "claude"); err != nil {
			logging.Warn("Failed to remove Claude provider marker", "error", err)
		}

		trackProviderEvent("disconnected", "oauth")

		if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
			logging.Warn("Failed to emit config_health refetch after Claude disconnect", "error", err)
		}

		return connect.NewResponse(&reliantv1.UpdateProviderAPIKeyResponse{
			Success: true,
			Message: "Disconnected from Claude",
		}), nil
	}

	if provider == "codex" {
		if strings.TrimSpace(req.Msg.ApiKey) != "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("codex does not use manual API keys. Use Login with Codex in Settings"))
		}

		if err := s.database.DeleteCodexAuthTokens(ctx, userID); err != nil {
			logging.Error("Failed to delete Codex auth tokens", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to disconnect from Codex"))
		}
		if err := s.database.DeleteProviderAPIKey(ctx, userID, "codex"); err != nil {
			logging.Error("Failed to delete codex provider marker", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to disconnect from Codex"))
		}
		trackProviderEvent("disconnected", "oauth")

		if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
			logging.Warn("Failed to emit config_health refetch after codex disconnect", "error", err)
		}

		return connect.NewResponse(&reliantv1.UpdateProviderAPIKeyResponse{
			Success: true,
			Message: "Disconnected from Codex",
		}), nil
	}

	if len(req.Msg.ApiKey) == 0 {
		// Delete the API key
		if err := s.database.DeleteProviderAPIKey(ctx, userID, provider); err != nil {
			logging.Error("Failed to delete provider API key", "provider", provider, "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete API key"))
		}
		trackProviderEvent("deleted", "api_key")
	} else {
		// Validate the API key first
		valid, message := s.validateAPIKey(ctx, models.Family(provider), req.Msg.ApiKey)
		if !valid {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s", message))
		}

		// Save the API key to dedicated table
		if err := s.database.SetProviderAPIKey(ctx, userID, provider, req.Msg.ApiKey); err != nil {
			logging.Error("Failed to set provider API key", "provider", provider, "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save API key"))
		}
		trackProviderEvent("updated", "api_key")
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after UpdateProviderAPIKey", "error", err)
	}

	return connect.NewResponse(&reliantv1.UpdateProviderAPIKeyResponse{
		Success: true,
		Message: "API key updated successfully",
	}), nil
}

// ValidateProviderAPIKey validates an API key for a provider
func (s *SettingsService) ValidateProviderAPIKey(ctx context.Context, req *connect.Request[reliantv1.ValidateProviderAPIKeyRequest]) (*connect.Response[reliantv1.ValidateProviderAPIKeyResponse], error) {
	provider := models.Family(req.Msg.Provider)
	userID := auth.MustGetUserID(ctx)

	if provider == "claude" {
		tokens, err := s.database.GetClaudeAuthTokens(ctx, userID)
		if err != nil {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{
				Valid:   false,
				Message: "Failed to load Claude credentials",
			}), nil
		}
		if tokens == nil || strings.TrimSpace(tokens.AccessToken) == "" {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{
				Valid:   false,
				Message: "Claude is not connected",
			}), nil
		}
		if claude.IsTokenExpired(tokens.ExpiresAt) {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{
				Valid:   false,
				Message: "Claude session expired. Please reconnect Claude.",
			}), nil
		}
		return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{
			Valid:   true,
			Message: "Connected to Claude",
		}), nil
	}

	if provider == "codex" {
		tokens, err := s.database.GetCodexAuthTokens(ctx, userID)
		if err != nil {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{
				Valid:   false,
				Message: "Failed to load Codex credentials",
			}), nil
		}
		if tokens == nil || strings.TrimSpace(tokens.AccessToken) == "" {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{
				Valid:   false,
				Message: "Codex is not connected",
			}), nil
		}
		if codex.IsTokenExpired(tokens.AccessToken) {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{
				Valid:   false,
				Message: "Codex session expired. Please reconnect Codex.",
			}), nil
		}
		return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{
			Valid:   true,
			Message: "Connected to Codex",
		}), nil
	}

	valid, message := s.validateAPIKey(ctx, provider, req.Msg.ApiKey)

	return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{
		Valid:   valid,
		Message: message,
	}), nil
}

func (s *SettingsService) CompleteCodexOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteCodexOAuthRequest]) (*connect.Response[reliantv1.CompleteCodexOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)

	if code == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("authorization code is required"))
	}
	if codeVerifier == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("code_verifier is required"))
	}
	if redirectURI == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("redirect_uri is required"))
	}

	tokens, err := codex.ExchangeCodexAuthorizationCode(code, codeVerifier, redirectURI)
	if err != nil {
		logging.Error("Codex OAuth code exchange failed", "error", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to complete Codex OAuth: %w", err))
	}

	if err := s.database.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		AccountID:    tokens.AccountID,
	}); err != nil {
		logging.Error("Failed to persist Codex auth tokens", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save Codex credentials"))
	}

	if err := s.database.SetProviderAPIKey(ctx, userID, "codex", "oauth"); err != nil {
		logging.Error("Failed to set Codex provider marker", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to connect Codex provider"))
	}

	analytics.GetClientForUser(ctx, userID).TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{
		Provider:   "codex",
		Action:     "connected",
		AuthMethod: "oauth",
	})

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after Codex OAuth connect", "error", err)
	}

	return connect.NewResponse(&reliantv1.CompleteCodexOAuthResponse{
		Success: true,
		Message: "Connected to Codex",
	}), nil
}

func (s *SettingsService) CompleteClaudeOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteClaudeOAuthRequest]) (*connect.Response[reliantv1.CompleteClaudeOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)
	state := strings.TrimSpace(req.Msg.State)

	if code == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("authorization code is required"))
	}
	if codeVerifier == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("code_verifier is required"))
	}
	if redirectURI == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("redirect_uri is required"))
	}

	tokens, err := claude.ExchangeClaudeAuthorizationCode(code, codeVerifier, redirectURI, state)
	if err != nil {
		logging.Error("Claude OAuth code exchange failed", "error", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to complete Claude OAuth: %w", err))
	}

	if err := s.database.SetClaudeAuthTokens(ctx, userID, db.ClaudeAuthTokens{
		AccessToken:      tokens.AccessToken,
		RefreshToken:     tokens.RefreshToken,
		ExpiresAt:        tokens.ExpiresAt,
		AccountUUID:      tokens.AccountUUID,
		AccountEmail:     tokens.AccountEmail,
		OrganizationUUID: tokens.OrganizationUUID,
		OrganizationName: tokens.OrganizationName,
		Scope:            tokens.Scope,
	}); err != nil {
		logging.Error("Failed to persist Claude auth tokens", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save Claude credentials"))
	}

	if err := s.database.SetProviderAPIKey(ctx, userID, "claude", "oauth"); err != nil {
		logging.Error("Failed to set Claude provider marker", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to connect Claude provider"))
	}

	analytics.GetClientForUser(ctx, userID).TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{
		Provider:   "claude",
		Action:     "connected",
		AuthMethod: "oauth",
	})

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after Claude OAuth connect", "error", err)
	}

	return connect.NewResponse(&reliantv1.CompleteClaudeOAuthResponse{
		Success: true,
		Message: "Connected to Claude",
	}), nil
}

// SyncReliantProvider mints (or rehydrates) the user's internal Reliant API key
// via control-plane and persists it locally as the "reliant" provider credential.
func (s *SettingsService) SyncReliantProvider(ctx context.Context, req *connect.Request[reliantv1.SyncReliantProviderRequest]) (*connect.Response[reliantv1.SyncReliantProviderResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if s.controlPlaneClient == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("control-plane client not configured"))
	}

	jwt, ok := auth.GetUserJWT(userID)
	if !ok || strings.TrimSpace(jwt) == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("missing user JWT for control-plane call"))
	}

	plaintext, err := s.controlPlaneClient.IssueMyReliantAPIKey(ctx, jwt)
	if err != nil {
		logging.Error("Failed to issue Reliant API key via control-plane", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to issue Reliant API key: %w", err))
	}
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("control-plane returned empty Reliant API key"))
	}

	previous, _ := s.database.GetProviderAPIKey(ctx, userID, "reliant")
	if err := s.database.SetProviderAPIKey(ctx, userID, "reliant", plaintext); err != nil {
		logging.Error("Failed to persist Reliant provider API key", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to persist Reliant API key"))
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after SyncReliantProvider", "error", err)
	}

	created := strings.TrimSpace(previous) == ""
	rotated := !created && strings.TrimSpace(previous) != plaintext

	maskedKey := ""
	if len(plaintext) > 8 {
		maskedKey = plaintext[:4] + "..." + plaintext[len(plaintext)-4:]
	} else {
		maskedKey = "***"
	}

	return connect.NewResponse(&reliantv1.SyncReliantProviderResponse{
		Success:    true,
		Message:    "Reliant provider synced",
		Synced:     true,
		CreatedKey: created,
		RotatedKey: rotated,
		Provider: &reliantv1.ProviderStatus{
			Provider:    "reliant",
			DisplayName: "Reliant",
			Configured:  true,
			HasApiKey:   true,
			MaskedKey:   &maskedKey,
		},
	}), nil
}

// ============================================================================
// Sub-phase 6e: Privacy Settings
// ============================================================================

// GetPrivacySettings retrieves user privacy settings
func (s *SettingsService) GetPrivacySettings(ctx context.Context, req *connect.Request[reliantv1.GetPrivacySettingsRequest]) (*connect.Response[reliantv1.GetPrivacySettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	analyticsEnabled, crashReportingEnabled, err := s.database.GetPrivacySettings(ctx, userID)
	if err != nil {
		logging.Error("Failed to get privacy settings", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get privacy settings"))
	}

	return connect.NewResponse(&reliantv1.GetPrivacySettingsResponse{
		AnalyticsEnabled:      analyticsEnabled,
		CrashReportingEnabled: crashReportingEnabled,
	}), nil
}

// UpdatePrivacySettings updates user privacy settings
func (s *SettingsService) UpdatePrivacySettings(ctx context.Context, req *connect.Request[reliantv1.UpdatePrivacySettingsRequest]) (*connect.Response[reliantv1.UpdatePrivacySettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get current settings
	analyticsEnabled, crashReportingEnabled, err := s.database.GetPrivacySettings(ctx, userID)
	if err != nil {
		logging.Error("Failed to get current privacy settings", "error", err)
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
	if err := s.database.SetPrivacySettings(ctx, userID, analyticsEnabled, crashReportingEnabled); err != nil {
		logging.Error("Failed to save privacy settings", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save privacy settings"))
	}

	logging.Info("Privacy settings updated", "userID", userID, "analyticsEnabled", analyticsEnabled, "crashReportingEnabled", crashReportingEnabled)

	return connect.NewResponse(&reliantv1.UpdatePrivacySettingsResponse{
		Success:               true,
		Message:               "Privacy settings updated successfully",
		AnalyticsEnabled:      analyticsEnabled,
		CrashReportingEnabled: crashReportingEnabled,
		RequiresRestart:       false,
	}), nil
}

// TrackPageVisited tracks a page visit for analytics
func (s *SettingsService) TrackPageVisited(ctx context.Context, req *connect.Request[reliantv1.TrackPageVisitedRequest]) (*connect.Response[reliantv1.TrackPageVisitedResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get analytics client that respects user's privacy settings
	analyticsClient := analytics.GetClientForUser(ctx, userID)

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
// func (s *SettingsService) TrackOnboardingEvent(ctx context.Context, req *connect.Request[reliantv1.TrackOnboardingEventRequest]) (*connect.Response[reliantv1.TrackOnboardingEventResponse], error) {
// 	userID := auth.MustGetUserID(ctx)
// 	analyticsClient := analytics.GetClientForUser(ctx, userID)
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
func (s *SettingsService) GetConfigHealth(
	ctx context.Context,
	req *connect.Request[reliantv1.GetConfigHealthRequest],
) (*connect.Response[reliantv1.GetConfigHealthResponse], error) {
	collector := validation.Global()

	// Get all errors from the collector
	errors := collector.Errors()

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
