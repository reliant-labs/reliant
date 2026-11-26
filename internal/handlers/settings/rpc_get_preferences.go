package settings

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/workflow"
)

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
	inboxShowHousekeeping := boolPref(inboxShowHousekeepingKey, false)
	inboxShowEveryAutomationFailure := boolPref(inboxShowEveryAutomationFailureKey, false)

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
		InboxShowHousekeeping:             inboxShowHousekeeping,
		InboxShowEveryAutomationFailure:   inboxShowEveryAutomationFailure,
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
