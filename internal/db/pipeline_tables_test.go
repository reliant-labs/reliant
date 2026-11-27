package db

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TestPipelineTablesConstraints proves, against a database, what
// db/migrations/20261125211730_pipeline_repos_and_settings.up.sql enforces:
// the defaults, the closed-set CHECKs refusing a value outside their Go set,
// and one enrolment per repository. The hermetic pin that each CHECK lists
// exactly its Go set is `forge lint --closed-sets`.
func TestPipelineTablesConstraints(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	const project = "test-project"
	insertRepo := func(id, repositoryID, mode string) error {
		_, err := raw.Exec(
			`INSERT INTO pipeline_repos (id, org_id, project_id, provider, account_key, repository_id, repository, mode, created_by_user_id)
			 VALUES ($1, 'org-1', $2, 'github', 'inst-1', $3, 'acme/app', $4, 'user-1')`,
			id, project, repositoryID, mode)
		return err
	}

	for _, mode := range core.AllPipelineRepoModes {
		require.NoError(t, insertRepo("repo-"+string(mode), "rid-"+string(mode), string(mode)), "mode %q", mode)
	}

	err := insertRepo("repo-bogus", "rid-bogus", "active")
	require.Error(t, err, "a mode outside the closed set is refused")
	assert.Equal(t, "pipeline_repos_mode", orm.ConstraintName(err))

	err = insertRepo("repo-dup", "rid-off", string(core.PipelineRepoModeOff))
	require.Error(t, err, "a repository is enrolled once per provider")
	assert.Equal(t, "pipeline_repos_provider_repository_key", orm.ConstraintName(err))

	_, err = raw.Exec(
		`INSERT INTO pipeline_repos (id, org_id, project_id, provider, account_key, repository_id, repository, created_by_user_id)
		 VALUES ('repo-defaults', 'org-1', $1, 'github', 'inst-1', 'rid-defaults', 'acme/lib', 'user-1')`, project)
	require.NoError(t, err)
	var baseBranch, mode, queueState string
	var version int64
	require.NoError(t, raw.QueryRow(
		`SELECT base_branch, mode, queue_state, version FROM pipeline_repos WHERE id = 'repo-defaults'`).
		Scan(&baseBranch, &mode, &queueState, &version))
	assert.Equal(t, "main", baseBranch)
	assert.Equal(t, string(core.PipelineRepoModeOff), mode, "an enrolment starts off")
	assert.Equal(t, string(core.PipelineQueueRunning), queueState)
	assert.Zero(t, version)

	_, err = raw.Exec(
		`INSERT INTO pipeline_project_settings (project_id, org_id, updated_by_user_id) VALUES ($1, 'org-1', 'user-1')`, project)
	require.NoError(t, err)
	var policy, releaseApproval string
	var dailyCap int
	require.NoError(t, raw.QueryRow(
		`SELECT emergency_land_policy, emergency_land_daily_cap, release_approval FROM pipeline_project_settings WHERE project_id = $1`, project).
		Scan(&policy, &dailyCap, &releaseApproval))
	assert.Equal(t, string(core.PipelineEmergencyLandAsk), policy)
	assert.Equal(t, 3, dailyCap)
	assert.Equal(t, string(core.PipelineReleaseApprovalProdOnly), releaseApproval)

	_, err = raw.Exec(`UPDATE pipeline_project_settings SET emergency_land_daily_cap = -1 WHERE project_id = $1`, project)
	require.Error(t, err, "a negative daily cap is refused")

	insertChange := func(id, via string) error {
		_, err := raw.Exec(
			`INSERT INTO pipeline_setting_changes (id, org_id, project_id, setting, old_value, new_value, changed_by_user_id, changed_via)
			 VALUES ($1, 'org-1', $2, 'emergency_land_policy', 'ask', 'auto_allow', 'user-1', $3)`,
			id, project, via)
		return err
	}
	for _, via := range core.AllPipelineChangedVia {
		if via == core.PipelineChangedViaConfig {
			continue // config changes only a gate stage: TestPipelineSettingChangesGateStages
		}
		require.NoError(t, insertChange("change-"+string(via), string(via)), "changed_via %q", via)
	}
	err = insertChange("change-api", "api")
	require.Error(t, err, "a setting changed by anything but a signed-in person's surface is refused")
	assert.Equal(t, "pipeline_setting_changes_changed_via", orm.ConstraintName(err))
}
