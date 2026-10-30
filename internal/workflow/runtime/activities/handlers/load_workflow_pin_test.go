// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaltest "go.temporal.io/sdk/testsuite"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// A run's root load pins the catalog it resolved against, and every later
// load of the run passes the pin: a child or preset loaded after a machine
// pushed a new version is the one the run started with. Another machine's
// push never leaks in either, and a pin that is gone fails closed rather than
// running whatever is current.
func TestLoadWorkflowActivity_PinnedRunLoadsTheCatalogItStartedWith(t *testing.T) {
	t.Parallel()

	repo := db.NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	userID := "user-" + uuid.NewString()
	projectID := "project-" + uuid.NewString()
	cicd, laptop := uuid.NewString(), uuid.NewString()
	for _, id := range []string{cicd, laptop} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: userID}))
	}
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "labs", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	push := func(daemonID string, at time.Time, version string) {
		t.Helper()
		release := "name: release\ndescription: " + version + "\napiVersion: v2\nentry: [s]\nnodes:\n  - id: s\n    type: workflow\n    ref: step\n    presets: {agent: impl}\n"
		step := "name: step\ndescription: " + version + "\napiVersion: v2\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"
		wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{
			{Slug: "release", Name: "release", RelativePath: ".reliant/workflows/release.yaml", YAMLContent: release},
			{Slug: "step", Name: "step", RelativePath: ".reliant/workflows/step.yaml", YAMLContent: step},
		})
		require.NoError(t, err)
		pj, err := config.EncodeCatalogJSON([]config.StoredPreset{
			{Name: "impl", RelativePath: ".reliant/presets/impl.yaml", YAMLContent: "name: impl\nparams:\n  version: " + version + "\n"},
		})
		require.NoError(t, err)
		_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: projectID, DaemonID: daemonID,
			ProjectWorkflowsJSON: wj, ProjectPresetsJSON: pj, PushedAt: at,
		}, true)
		require.NoError(t, err)
	}
	push(cicd, now.Add(-time.Hour), "cicd-v1")
	push(laptop, now.Add(-time.Minute), "laptop")

	chatID := "chat-" + uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: userID, ProjectID: projectID, Title: "run",
		State: db.ChatStateIdle, ActiveDaemonID: &cicd,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	activity := NewLoadWorkflowActivity(repo)

	root, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "release"})
	require.NoError(t, err)
	require.NotEmpty(t, root.CatalogPin, "a root load pins its catalog")
	assert.Contains(t, string(root.YAML), "description: cicd-v1")
	again, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "release"})
	require.NoError(t, err)
	assert.Equal(t, root.CatalogPin, again.CatalogPin, "an unchanged catalog shares its pin")

	// cicd pushes a new version mid-run, and the laptop pushes last.
	push(cicd, now, "cicd-v2")
	push(laptop, now.Add(time.Minute), "laptop-v2")

	child, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "step", CatalogPin: root.CatalogPin})
	require.NoError(t, err)
	assert.Contains(t, string(child.YAML), "description: cicd-v1", "the child the run started with, not cicd's new push")
	assert.Equal(t, root.CatalogPin, child.CatalogPin, "a later load carries the run's pin on")

	presets := NewLoadPresetParamsActivity(repo)
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(presets.Execute)
	presetVersion := func(pin string) any {
		t.Helper()
		val, err := env.ExecuteActivity(presets.Execute, LoadPresetParamsInput{ChatID: chatID, PresetName: "impl", CatalogPin: pin})
		require.NoError(t, err)
		var params map[string]interface{}
		require.NoError(t, val.Get(&params))
		return params["version"]
	}
	assert.Equal(t, "cicd-v1", presetVersion(root.CatalogPin), "the preset the run started with")
	assert.Equal(t, "cicd-v2", presetVersion(""), "an unpinned load reads the machine's current preset")

	fresh, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "release"})
	require.NoError(t, err)
	assert.Contains(t, string(fresh.YAML), "description: cicd-v2", "a new run starts from the current catalog")
	assert.NotEqual(t, root.CatalogPin, fresh.CatalogPin)

	_, err = activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "step", CatalogPin: "0000unknown"})
	require.Error(t, err, "a pin that is gone fails closed")
	assert.Contains(t, err.Error(), "no longer stored")

	// The owner's account workflows are part of the catalog too: one saved
	// after the run started, under a name the run's tree loads, does not
	// replace the definition the run started with, and one the run started
	// with still resolves after the owner edits it.
	now = time.Now().UTC()
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: userID, Name: "step", Slug: "step", Version: 1,
		Definition: "name: step\ndescription: account-added-mid-run\napiVersion: v2\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n",
		Status:     db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now,
	}))
	child, err = activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "step", CatalogPin: root.CatalogPin})
	require.NoError(t, err)
	assert.Contains(t, string(child.YAML), "description: cicd-v1", "an account workflow saved mid-run does not replace the pinned child")
	unpinned, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "step"})
	require.NoError(t, err)
	assert.Contains(t, string(unpinned.YAML), "account-added-mid-run", "a new run resolves the account workflow first, as before")

	stepPin := unpinned.CatalogPin
	draft, err := repo.GetWorkflowDraftBySlug(ctx, userID, "step")
	require.NoError(t, err)
	draft.Definition = strings.Replace(draft.Definition, "account-added-mid-run", "account-edited", 1)
	require.NoError(t, repo.UpdateWorkflowDraft(ctx, draft))
	child, err = activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "step", CatalogPin: stepPin})
	require.NoError(t, err)
	assert.Contains(t, string(child.YAML), "account-added-mid-run", "an edit to the account workflow does not reach a run pinned before it")
}
