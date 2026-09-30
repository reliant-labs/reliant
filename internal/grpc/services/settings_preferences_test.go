package services

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingSettingsRepo counts the read calls GetPreferences makes so the test
// can pin the number of database round trips, not just the response. The
// handler used to issue one GetSetting per typed preference; a regression back
// to that shape is invisible in the response but is the whole cost.
type countingSettingsRepo struct {
	db.Repository

	getSettingCalls   atomic.Int32
	listSettingsCalls atomic.Int32
}

func (r *countingSettingsRepo) GetSetting(ctx context.Context, userID string, projectID *string, key string) (*db.Setting, error) {
	r.getSettingCalls.Add(1)
	return r.Repository.GetSetting(ctx, userID, projectID, key)
}

func (r *countingSettingsRepo) ListSettings(ctx context.Context, userID string, projectID *string) ([]*db.Setting, error) {
	r.listSettingsCalls.Add(1)
	return r.Repository.ListSettings(ctx, userID, projectID)
}

func writeSetting(t *testing.T, ctx context.Context, repo db.Repository, projectID *string, key, value string) {
	t.Helper()
	require.NoError(t, repo.CreateSetting(ctx, &db.Setting{
		ID:        uuid.New().String(),
		UserID:    "test-user",
		ProjectID: projectID,
		Key:       key,
		Value:     value,
		ValueType: "string",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}))
}

func writeUserSetting(t *testing.T, ctx context.Context, repo db.Repository, key, value string) {
	t.Helper()
	writeSetting(t, ctx, repo, nil, key, value)
}

// Every typed preference is read from a user-global (project_id IS NULL)
// setting, and a missing key falls back to the documented default.
func TestSettingsService_GetPreferences_ReturnsDefaultsWhenNothingIsStored(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	resp, err := svc.GetPreferences(ctx, connect.NewRequest(&reliantv1.GetPreferencesRequest{}))
	require.NoError(t, err)

	msg := resp.Msg
	assert.True(t, msg.StreamingEnabled, "streaming defaults on")
	assert.Equal(t, "ask_me", msg.WorktreeArchiveMode)
	assert.True(t, msg.WorktreeDefaultDeleteDirectory)
	assert.False(t, msg.WorktreeDefaultDeleteBranch)
	assert.False(t, msg.BranchCopyUncommittedFilesDefault)
	assert.Equal(t, reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT, msg.DefaultMcpScope)
	assert.Equal(t, reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT, msg.DefaultWorkflowScope)
	assert.Equal(t, workflow.DefaultWorkflow, msg.DefaultWorkflow)
	assert.False(t, msg.HideBuiltinWorkflows)
	assert.False(t, msg.HideBuiltinPresets)
	assert.Empty(t, msg.Additional)
}

func TestSettingsService_GetPreferences_ReturnsStoredValues(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	writeUserSetting(t, ctx, repo, "features.streaming_enabled", "false")
	writeUserSetting(t, ctx, repo, "worktree.archive_cleanup_mode", "always")
	writeUserSetting(t, ctx, repo, "worktree.default_delete_directory", "false")
	writeUserSetting(t, ctx, repo, "worktree.default_delete_branch", "true")
	writeUserSetting(t, ctx, repo, "worktree.branch_copy_uncommitted_files_default", "true")
	writeUserSetting(t, ctx, repo, "config.default_mcp_scope", "CONFIG_SCOPE_GLOBAL")
	writeUserSetting(t, ctx, repo, "config.default_workflow_scope", "CONFIG_SCOPE_GLOBAL")
	writeUserSetting(t, ctx, repo, "config.default_workflow", "builtin://custom")
	writeUserSetting(t, ctx, repo, "ui.hide_builtin_workflows", "true")
	writeUserSetting(t, ctx, repo, "ui.hide_builtin_presets", "true")

	resp, err := svc.GetPreferences(ctx, connect.NewRequest(&reliantv1.GetPreferencesRequest{}))
	require.NoError(t, err)

	msg := resp.Msg
	assert.False(t, msg.StreamingEnabled)
	assert.Equal(t, "always", msg.WorktreeArchiveMode)
	assert.False(t, msg.WorktreeDefaultDeleteDirectory)
	assert.True(t, msg.WorktreeDefaultDeleteBranch)
	assert.True(t, msg.BranchCopyUncommittedFilesDefault)
	assert.Equal(t, reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL, msg.DefaultMcpScope)
	assert.Equal(t, reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL, msg.DefaultWorkflowScope)
	assert.Equal(t, "builtin://custom", msg.DefaultWorkflow)
	assert.True(t, msg.HideBuiltinWorkflows)
	assert.True(t, msg.HideBuiltinPresets)
}

// A stored value that does not parse falls back to the default rather than
// surfacing an error or a zero enum.
func TestSettingsService_GetPreferences_UnparseableValuesFallBackToDefaults(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	writeUserSetting(t, ctx, repo, "config.default_mcp_scope", "NOT_A_SCOPE")
	writeUserSetting(t, ctx, repo, "config.default_workflow_scope", "NOT_A_SCOPE")
	// An empty default_workflow is explicitly ignored: the handler only takes
	// the stored value when it is non-empty.
	writeUserSetting(t, ctx, repo, "config.default_workflow", "")
	// Anything other than "true" is false for the boolean preferences.
	writeUserSetting(t, ctx, repo, "features.streaming_enabled", "yes")

	resp, err := svc.GetPreferences(ctx, connect.NewRequest(&reliantv1.GetPreferencesRequest{}))
	require.NoError(t, err)

	msg := resp.Msg
	assert.Equal(t, reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT, msg.DefaultMcpScope)
	assert.Equal(t, reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT, msg.DefaultWorkflowScope)
	assert.Equal(t, workflow.DefaultWorkflow, msg.DefaultWorkflow)
	assert.False(t, msg.StreamingEnabled)
}

// Arbitrary "preference.*" keys are returned in Additional with the prefix
// stripped, except the two that have dedicated proto fields.
func TestSettingsService_GetPreferences_AdditionalStripsPrefixAndExcludesDedicatedKeys(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	writeUserSetting(t, ctx, repo, "preference.theme", "dark")
	writeUserSetting(t, ctx, repo, "preference.default_planning_mode", "plan")
	writeUserSetting(t, ctx, repo, "preference.default_auto_approve", "true")
	// Not a preference.* key, so it must not leak into Additional.
	writeUserSetting(t, ctx, repo, "appearance.fontSize", "14")
	// The exact prefix with nothing after it is shorter than the guard length
	// and is dropped.
	writeUserSetting(t, ctx, repo, "preference.", "x")

	resp, err := svc.GetPreferences(ctx, connect.NewRequest(&reliantv1.GetPreferencesRequest{}))
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"theme": "dark"}, resp.Msg.Additional)
}

// Project-scoped rows must NOT satisfy a user-global preference read: every
// GetSetting here passes a nil projectID, which matches project_id IS NULL only.
func TestSettingsService_GetPreferences_IgnoresProjectScopedRows(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	projectID := "test-project"
	writeSetting(t, ctx, repo, &projectID, "worktree.archive_cleanup_mode", "always")
	writeSetting(t, ctx, repo, &projectID, "preference.theme", "dark")

	resp, err := svc.GetPreferences(ctx, connect.NewRequest(&reliantv1.GetPreferencesRequest{}))
	require.NoError(t, err)

	assert.Equal(t, "ask_me", resp.Msg.WorktreeArchiveMode, "project-scoped row must not win a user-global read")
	assert.Empty(t, resp.Msg.Additional, "project-scoped preference.* must not appear in the user-global response")
}

// The typed preferences all come from one user-global scope, so they must be
// fetched in a single query rather than one round trip per key. Eleven serial
// round trips is what made this RPC average ~466ms.
func TestSettingsService_GetPreferences_ReadsSettingsInOneQuery(t *testing.T) {
	base, cleanup := db.SetupTestDB(t)
	defer cleanup()

	repo := &countingSettingsRepo{Repository: base}
	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	writeUserSetting(t, ctx, base, "features.streaming_enabled", "false")
	writeUserSetting(t, ctx, base, "preference.theme", "dark")

	resp, err := svc.GetPreferences(ctx, connect.NewRequest(&reliantv1.GetPreferencesRequest{}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.StreamingEnabled)
	assert.Equal(t, map[string]string{"theme": "dark"}, resp.Msg.Additional)

	assert.Equal(t, int32(0), repo.getSettingCalls.Load(), "no per-key GetSetting round trips")
	assert.Equal(t, int32(1), repo.listSettingsCalls.Load(), "exactly one settings query")
}
