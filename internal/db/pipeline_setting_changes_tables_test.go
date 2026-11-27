package db

import (
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TestPipelineSettingChangesGateStages proves, against a database, what
// db/migrations/20261126164236_pipeline_setting_changes_gate_stages.up.sql enforces: every setting and surface of the
// widened closed sets is accepted, a gate stage changes only through config
// and config changes only a gate stage, and the actor rule — a web or
// desktop change names its Reliant user, a config change names the merging
// PR's provider author (and the Reliant user only when one maps) and the
// commit that carried it. The hermetic pin that each CHECK lists exactly its
// Go set is `forge lint --closed-sets`.
func TestPipelineSettingChangesGateStages(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	const project = "test-project"
	exec := func(query string, args ...any) error {
		_, err := raw.Exec(query, args...)
		return err
	}
	refused := func(t *testing.T, err error, constraint, why string) {
		t.Helper()
		require.Error(t, err, why)
		assert.Equal(t, constraint, orm.ConstraintName(err), why)
	}
	require.NoError(t, exec(
		`INSERT INTO pipeline_repos (id, org_id, project_id, provider, account_key, repository_id, repository, created_by_user_id)
		 VALUES ('repo-1', 'org-1', $1, 'github', 'inst-1', 'rid-1', 'acme/app', 'user-1')`, project))
	require.NoError(t, exec(
		`INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state, snapshot_at, observed_at)
		 VALUES ('rec-1', 'github', 'inst-1', 'rid-1', 'acme/app', 1, 'merged', now(), now())`))

	// c is one pipeline_setting_changes row.
	type c struct {
		id, setting, via                         string
		user, sha, record, provider, puid, login any
	}
	insert := func(r c) error {
		return exec(
			`INSERT INTO pipeline_setting_changes (id, org_id, project_id, setting, old_value, new_value, changed_by_user_id, changed_via,
			   repo_id, config_sha, config_record_id, changed_by_provider, changed_by_provider_user_id, changed_by_login)
			 VALUES ($1, 'org-1', $2, $3, 'on', 'off', $4, $5, 'repo-1', $6, $7, $8, $9, $10)`,
			r.id, project, r.setting, r.user, r.via, r.sha, r.record, r.provider, r.puid, r.login)
	}
	byAdmin := func(id string, setting core.PipelineSetting, via core.PipelineChangedVia) c {
		return c{id: id, setting: string(setting), via: string(via), user: "admin-1"}
	}
	byConfig := func(id string, setting core.PipelineSetting) c {
		return c{id: id, setting: string(setting), via: string(core.PipelineChangedViaConfig),
			sha: "sha-1", record: "rec-1", provider: "github", puid: "1234", login: "octocat"}
	}
	isStage := func(s core.PipelineSetting) bool { return slices.Contains(core.PipelineGateStageSettings, s) }

	t.Run("every setting through its surface", func(t *testing.T) {
		for _, s := range core.AllPipelineSettings {
			if isStage(s) {
				require.NoError(t, insert(byConfig("cfg-"+string(s), s)), "stage %q changes through config", s)
				continue
			}
			for _, via := range core.AllPipelineChangedVia {
				if via == core.PipelineChangedViaConfig {
					continue
				}
				require.NoError(t, insert(byAdmin("adm-"+string(s)+"-"+string(via), s, via)), "%q via %q", s, via)
			}
		}
		require.Len(t, core.PipelineGateStageSettings, 4)
		for _, s := range core.PipelineGateStageSettings {
			require.True(t, strings.HasPrefix(string(s), "gate.stage."), s)
		}

		r := byAdmin("bad-setting", "gate.stage.deploy", core.PipelineChangedViaWeb)
		refused(t, insert(r), "pipeline_setting_changes_setting", "a setting outside the set")
		r = byAdmin("bad-via", core.PipelineSettingReleaseApproval, "api")
		refused(t, insert(r), "pipeline_setting_changes_changed_via", "a surface outside the set")
	})

	t.Run("a gate stage changes only through config, and config only a gate stage", func(t *testing.T) {
		for _, s := range core.AllPipelineSettings {
			if isStage(s) {
				refused(t, insert(byAdmin("web-"+string(s), s, core.PipelineChangedViaWeb)),
					"pipeline_setting_changes_config", "an admin does not turn a stage off in the app, pipeline.yaml does: "+string(s))
				continue
			}
			refused(t, insert(byConfig("cfg-"+string(s), s)),
				"pipeline_setting_changes_config", "a repository's config never changes a project setting: "+string(s))
		}
	})

	t.Run("actors", func(t *testing.T) {
		require.NoError(t, insert(byConfig("cfg-nouser", core.PipelineSettingGateStageLint)),
			"a config change needs no Reliant user: the PR author may have none")
		r := byConfig("cfg-mapped", core.PipelineSettingGateStageLint)
		r.user = "user-7"
		require.NoError(t, insert(r), "and names one when the author maps to one")
		r = byConfig("cfg-norecord", core.PipelineSettingGateStageTest)
		r.record = nil
		require.NoError(t, insert(r), "the PR is named when known")

		for _, missing := range []string{"sha", "provider", "puid", "login"} {
			r := byConfig("cfg-no-"+missing, core.PipelineSettingGateStageBuild)
			switch missing {
			case "sha":
				r.sha = nil
			case "provider":
				r.provider = ""
			case "puid":
				r.puid = nil
			case "login":
				r.login = ""
			}
			refused(t, insert(r), "pipeline_setting_changes_actor", "a config change names its commit and provider author: "+missing)
		}

		r = byAdmin("web-nouser", core.PipelineSettingReleaseApproval, core.PipelineChangedViaWeb)
		r.user = nil
		refused(t, insert(r), "pipeline_setting_changes_actor", "a web change still names its Reliant user")
		r = byAdmin("web-login", core.PipelineSettingReleaseApproval, core.PipelineChangedViaDesktop)
		r.login, r.provider, r.puid = "octocat", "github", "1234"
		refused(t, insert(r), "pipeline_setting_changes_actor", "a person's change in the app names no provider author")
		r = byAdmin("web-sha", core.PipelineSettingReleaseApproval, core.PipelineChangedViaWeb)
		r.sha = "sha-1"
		refused(t, insert(r), "pipeline_setting_changes_actor", "nor a config commit")

		refused(t, exec(`INSERT INTO pipeline_setting_changes (id, org_id, project_id, setting, new_value, changed_by_user_id, changed_via)
			VALUES ('blank-user', 'org-1', $1, 'release_approval', 'none', '', 'web')`, project),
			"pipeline_setting_changes_changed_by_user_id_check", "a blank user id is still refused")
	})

	t.Run("history outlives the PR record and the enrolment", func(t *testing.T) {
		require.NoError(t, exec(`DELETE FROM pr_records WHERE id = 'rec-1'`))
		require.NoError(t, exec(`DELETE FROM pipeline_repos WHERE id = 'repo-1'`))
		var n int
		require.NoError(t, raw.QueryRow(`SELECT count(*) FROM pipeline_setting_changes WHERE changed_via = 'config' AND repo_id IS NULL AND config_record_id IS NULL`).Scan(&n))
		assert.Equal(t, 7, n, "the four stage changes and the three actor cases")
	})
}
