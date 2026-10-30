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
	"github.com/reliant-labs/reliant/internal/db/core"
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

// A selected preset only another machine synced is not this run's: the
// launch is refused before anything is stored, rather than run on schema
// defaults without the preset's tools, prompt and provider policy.
func TestLaunchRefusesAPresetOnlyAnotherMachineSynced(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	cicd, laptop := uuid.NewString(), uuid.NewString()
	for _, id := range []string{cicd, laptop} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: launchTestUserID}))
	}
	now := time.Now().UTC()
	for daemonID, presets := range map[string][]config.StoredPreset{
		cicd:   {{Name: "other", RelativePath: ".reliant/presets/other.yaml", YAMLContent: "name: other\ntag: agent\nparams:\n  mode: plan\n"}},
		laptop: {{Name: "laptop-only", RelativePath: ".reliant/presets/laptop-only.yaml", YAMLContent: "name: laptop-only\ntag: agent\nparams:\n  mode: auto\n"}},
	} {
		pj, err := config.EncodeCatalogJSON(presets)
		require.NoError(t, err)
		_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: projectID, DaemonID: daemonID, ProjectPresetsJSON: pj, PushedAt: now,
		}, true)
		require.NoError(t, err)
	}

	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	spec := func(daemonID string) Spec {
		return Spec{
			OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent", DaemonID: daemonID,
			Presets: map[string]string{"default": "laptop-only"},
			Params:  mockModelParams(t), Messages: userSeed("go"),
		}
	}
	_, err := launcher.Launch(ctx, chatStartEvent(), spec(cicd))
	require.Error(t, err, "cicd never synced laptop-only")
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, ValidationFailedPrecondition, ve.Kind)
	assert.Contains(t, ve.Reason, `"laptop-only"`)
	assert.Empty(t, starter.calls, "nothing started")

	_, err = launcher.Launch(ctx, chatStartEvent(), spec(laptop))
	require.NoError(t, err, "on the laptop the preset resolves")
	assert.Equal(t, "auto", starter.lastRootInput(t).Inputs["mode"])
}

// A pending no-machine branch started with a daemon named at its first send
// is admitted against that daemon's catalog: its tree, presets and input
// schema. The input check used to keep the chat's no-machine flag and read
// the union, so the laptop's newer schema rejected a valid start on cicd.
func TestLaunchPendingNoMachineBranchStartedOnAMachineReadsThatMachine(t *testing.T) {
	repo, ctx, projectID, mainWorktreeID := launchFixture(t)
	cicd, laptop := uuid.NewString(), uuid.NewString()
	for _, id := range []string{cicd, laptop} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: launchTestUserID}))
	}
	deploy := func(inputs string) *string {
		wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "deploy", Name: "deploy", RelativePath: ".reliant/workflows/deploy.yaml",
			YAMLContent: "name: deploy\napiVersion: v2\ninputs:\n" + inputs + "entry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"}})
		require.NoError(t, err)
		return wj
	}
	now := time.Now().UTC()
	for daemonID, push := range map[string]struct {
		at     time.Time
		inputs string
	}{
		cicd:   {now.Add(-time.Hour), "  env: {type: string, default: prod}\n"},
		laptop: {now, "  env: {type: string, required: true}\n"},
	} {
		_, err := repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: projectID, DaemonID: daemonID, ProjectWorkflowsJSON: deploy(push.inputs), PushedAt: push.at,
		}, true)
		require.NoError(t, err)
	}
	chatID := pendingBranch(t, repo, ctx, projectID, mainWorktreeID)
	_, err := repo.ORM().Exec(ctx, `UPDATE chats SET no_machine = true WHERE id = $1`, chatID)
	require.NoError(t, err)

	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	_, err = launcher.Launch(ctx, Event{Kind: core.TriggerEventKindChatStart, DedupeKey: chatID}, Spec{
		OwnerUserID: launchTestUserID, ChatID: chatID, Workflow: "deploy", DaemonID: cicd, Messages: userSeed("go"),
	})
	require.NoError(t, err, "the laptop's required env must not reject a start on cicd")
	assert.Equal(t, "prod", starter.lastRootInput(t).Inputs["env"])
}
