// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// The owner's existing activations of the Autopilot pack were written with
// `{}` params, when `repos` was an optional array. Health is computed on read
// from the workflow as it is now, so the pack's move to a required
// github.repository must turn those rows into NEEDS_SETUP with no data change.
// These tests build exactly those rows and read them through the same path the
// trigger handler renders with (ResolveDeclaration over the embedded builtin,
// then NeedsSetupHealth); nothing is migrated or written.

var autopilotRepoScoped = map[string][]string{
	"builtin://autopilot-pr-review":        {"pr-opened", "re-review-label", "re-review-comment"},
	"builtin://autopilot-review-responder": {"review-submitted", "review-comment", "pr-comment"},
	"builtin://autopilot-issue-worker":     {"ready-label", "mention"},
}

// legacyActivation is a row as the old pack's Enable wrote it.
func legacyActivation(workflow, trigger string, params map[string]any) *core.Trigger {
	return &core.Trigger{
		ID: "legacy-" + trigger, UserID: "owner", ProjectID: "project-1", Name: trigger,
		Kind: core.TriggerKindIntegration, Enabled: true, Workflow: workflow, WorkflowTrigger: &trigger,
		DaemonID: testDaemonID, Params: params,
		Config: json.RawMessage(`{"integration":"github","events":["pull_request.opened"]}`),
	}
}

func TestOwnersExistingEmptyAutopilotActivationsComputeNeedsSetup(t *testing.T) {
	for workflow, triggerNames := range autopilotRepoScoped {
		for _, name := range triggerNames {
			for label, params := range map[string]map[string]any{
				"null params":             nil,
				"{}":                      {},
				"repos: [] (old default)": {"repos": []any{}},
			} {
				t.Run(workflow+"/"+name+"/"+label, func(t *testing.T) {
					row := legacyActivation(workflow, name, params)
					decl, err := ResolveDeclaration(context.Background(), LaunchWorkflows{}, row)
					require.NoError(t, err, "the builtin resolves with no store, as it does in prod")

					health := NeedsSetupHealth(decl, row)
					require.NotNil(t, health, "an activation with no repos must read as needing setup")
					assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_NEEDS_SETUP, health.GetStatus())
					require.NotEmpty(t, health.GetSetupIssues())
					assert.Equal(t, "repos", health.GetSetupIssues()[0].GetInput())
					assert.Contains(t, health.GetLastFailureDetail(), "needs setup")

					// Only repos is outstanding: trusted_ids and the labels have defaults.
					for _, issue := range health.GetSetupIssues() {
						assert.Equal(t, "repos", issue.GetInput(), "unexpected setup issue %q", issue.GetMessage())
					}
				})
			}
		}
	}
}

func TestOwnersExistingAutopilotActivationWithReposStaysComplete(t *testing.T) {
	// An activation that had repos set (and optional trusted ids and a renamed
	// label) keeps working: the stored values satisfy the typed inputs.
	for workflow, triggerNames := range autopilotRepoScoped {
		for _, name := range triggerNames {
			row := legacyActivation(workflow, name, map[string]any{
				"repos":       []any{"acme/app", "acme/web"},
				"trusted_ids": []any{"583231"},
			})
			decl, err := ResolveDeclaration(context.Background(), LaunchWorkflows{}, row)
			require.NoError(t, err)
			assert.Nil(t, NeedsSetupHealth(decl, row), "%s/%s: a complete activation is not NEEDS_SETUP", workflow, name)
			assert.Empty(t, SetupIssues(decl, row))
		}
	}
}

func TestAutopilotReposAreScopedByTypedMatchInput(t *testing.T) {
	// The projection an activation stores carries the repos it holds, so an
	// event for another repository is dropped before a row is written.
	row := legacyActivation("builtin://autopilot-pr-review", "pr-opened", map[string]any{"repos": []any{"acme/app"}})
	decl, err := ResolveDeclaration(context.Background(), LaunchWorkflows{}, row)
	require.NoError(t, err)

	var cfg core.IntegrationConfig
	require.NoError(t, json.Unmarshal(decl.ProjectedConfig(row.Params), &cfg))
	assert.Equal(t, map[string]string{"repository": "repos"}, cfg.MatchInput)
	assert.Equal(t, map[string][]string{"repository": {"acme/app"}}, cfg.MatchAny)
	assert.True(t, IntegrationEventMatches(cfg, "pull_request.opened", map[string]string{"repository": "acme/app"}))
	assert.False(t, IntegrationEventMatches(cfg, "pull_request.opened", map[string]string{"repository": "acme/other"}))

	// The legacy row, with no repos, projects an empty scope that admits nothing.
	legacy := legacyActivation("builtin://autopilot-pr-review", "pr-opened", map[string]any{})
	require.NoError(t, json.Unmarshal(decl.ProjectedConfig(legacy.Params), &cfg))
	assert.False(t, IntegrationEventMatches(cfg, "pull_request.opened", map[string]string{"repository": "acme/app"}))
}

func TestAutopilotPackInputsAreTyped(t *testing.T) {
	for workflow := range autopilotRepoScoped {
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(workflow[len("builtin://"):] + ".yaml")
		require.NoError(t, err)
		wf, err := wfyaml.ParseWorkflow(data)
		require.NoError(t, err)

		repos := wf.GetInputs()["repos"]
		require.NotNil(t, repos, workflow)
		assert.Equal(t, "github.repository", repos.GetType(), workflow)
		assert.True(t, model.GetInputBase(repos).GetRequired(), workflow)
		assert.True(t, repos.GetIntegrationInput().GetMulti(), workflow)
		assert.EqualValues(t, 1, repos.GetIntegrationInput().GetMinItems(), workflow)
		assert.Nil(t, repos.GetIntegrationInput().GetDefault(), "a required input takes no default: %s", workflow)

		trusted := wf.GetInputs()["trusted_ids"]
		require.NotNil(t, trusted, workflow)
		assert.Equal(t, "github.user", trusted.GetType(), workflow)
		assert.True(t, trusted.GetIntegrationInput().GetMulti(), workflow)
		assert.False(t, model.GetInputBase(trusted).GetRequired(), workflow)
	}
}
