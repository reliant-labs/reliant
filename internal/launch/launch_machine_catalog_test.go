// Copyright (c) 2025 Reliant Labs
package launch

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// A launch on a machine builds its inputs from that machine's presets and
// admits its workflow against that machine's catalog, the same catalog the
// run's own loads read. The union is newest-push-wins per file, so a laptop
// that pushed last must not decide what a run on cicd executes.
func TestLaunchOnAMachineReadsThatMachinesCatalog(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	cicd, laptop := uuid.NewString(), uuid.NewString()
	for _, id := range []string{cicd, laptop} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: launchTestUserID}))
	}
	push := func(daemonID string, at time.Time, mode string, workflows []config.StoredWorkflow) {
		pj, err := config.EncodeCatalogJSON([]config.StoredPreset{
			{Name: "house", RelativePath: ".reliant/presets/house.yaml", YAMLContent: "name: house\ntag: agent\nparams:\n  mode: " + mode + "\n"},
		})
		require.NoError(t, err)
		wj, err := config.EncodeCatalogJSON(workflows)
		require.NoError(t, err)
		_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: projectID, DaemonID: daemonID, ProjectPresetsJSON: pj, ProjectWorkflowsJSON: wj, PushedAt: at,
		}, true)
		require.NoError(t, err)
	}
	now := time.Now().UTC()
	push(cicd, now.Add(-time.Hour), "plan", nil)
	push(laptop, now, "build", []config.StoredWorkflow{{
		Slug: "laptop-only", Name: "laptop-only", RelativePath: ".reliant/workflows/laptop-only.yaml",
		YAMLContent: "name: laptop-only\napiVersion: v2\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n",
	}})

	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent", DaemonID: cicd,
		Presets: map[string]string{"default": "house"},
		Params:  mockModelParams(t), Messages: userSeed("go"),
	})
	require.NoError(t, err)
	assert.Equal(t, "plan", starter.lastRootInput(t).Inputs["mode"], "cicd's preset, not the union's newest (the laptop's)")

	_, err = launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "laptop-only", DaemonID: cicd,
		Params: mockModelParams(t), Messages: userSeed("go"),
	})
	require.Error(t, err, "a workflow only the laptop provides cannot run on cicd")

	_, err = launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "laptop-only", DaemonID: laptop,
		Params: mockModelParams(t), Messages: userSeed("go"),
	})
	require.NoError(t, err, "on the laptop it runs")

	// Two definitions of one workflow with conflicting input schemas: cicd's
	// defaults env, the laptop's (newer, so the union's) requires it. A
	// launch on cicd is admitted, defaulted and validated against cicd's.
	deploy := func(inputs string) config.StoredWorkflow {
		return config.StoredWorkflow{Slug: "deploy", Name: "deploy", RelativePath: ".reliant/workflows/deploy.yaml",
			YAMLContent: "name: deploy\napiVersion: v2\ninputs:\n" + inputs + "entry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"}
	}
	push(cicd, now.Add(time.Minute), "plan", []config.StoredWorkflow{deploy("  env: {type: string, default: prod}\n")})
	push(laptop, now.Add(2*time.Minute), "build", []config.StoredWorkflow{deploy("  env: {type: string, required: true}\n  region: {type: string, default: eu}\n")})

	_, err = launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "deploy", DaemonID: cicd,
		Messages: userSeed("go"),
	})
	require.NoError(t, err, "the laptop's required env must not reject a launch on cicd")
	inputs := starter.lastRootInput(t).Inputs
	assert.Equal(t, "prod", inputs["env"], "cicd's default")
	assert.NotContains(t, inputs, "region", "the laptop's default is not cicd's")
}
