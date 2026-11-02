// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A run loads its workflow, its children and its presets from the catalog of
// the machine it executes on: the one a trigger was admitted against. The
// union of every machine's is newest-push-wins per file, so before this a
// check against cicd ran whatever the laptop pushed last.
func TestLoadWorkflowActivity_LoadsFromTheRunsMachine(t *testing.T) {
	t.Parallel()

	repo := db.NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	userID := "user-" + uuid.NewString()
	projectID := "project-" + uuid.NewString()
	cicd, laptop, fresh := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{cicd, laptop, fresh} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: userID}))
	}
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "labs", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	// release spawns its child "step" with preset "impl"; each machine holds
	// its own version of all three, at the same paths.
	release := func(model string) string {
		return `name: release
description: ` + model + `
apiVersion: v2
entry: [s]
nodes:
  - id: s
    type: workflow
    ref: step
    presets: {agent: impl}
`
	}
	step := func(content string) string {
		return "name: step\napiVersion: v2\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: " + content + "}\n"
	}
	push := func(daemonID string, at time.Time, version string, stepYAML string) {
		wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{
			{Slug: "release", Name: "release", RelativePath: "control-plane/.reliant/workflows/release.yaml", YAMLContent: release(version)},
			{Slug: "step", Name: "step", RelativePath: "control-plane/.reliant/workflows/step.yaml", YAMLContent: stepYAML},
		})
		require.NoError(t, err)
		pj, err := config.EncodeCatalogJSON([]config.StoredPreset{
			{Name: "impl", RelativePath: "control-plane/.reliant/presets/impl.yaml", YAMLContent: "name: impl\nparams:\n  version: " + version + "\n"},
		})
		require.NoError(t, err)
		_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: projectID, DaemonID: daemonID,
			ProjectWorkflowsJSON: wj, ProjectPresetsJSON: pj, PushedAt: at,
		}, true)
		require.NoError(t, err)
	}
	push(cicd, now.Add(-time.Hour), "cicd", step("from-cicd"))
	// The laptop pushed last, so the union is the laptop's, and its child is
	// broken: a run on cicd must neither run nor trip over it.
	push(laptop, now, "laptop", "name: step\napiVersion: v2\nentry: [missing]\nnodes: []\n")

	chatOn := func(daemonID *string) string {
		id := "chat-" + uuid.NewString()
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{
			ID: id, UserID: userID, ProjectID: projectID, Title: "run",
			State: db.ChatStateIdle, ActiveDaemonID: daemonID,
			CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		return id
	}
	activity := NewLoadWorkflowActivity(repo)

	t.Run("a run on cicd loads cicd's root, child and preset", func(t *testing.T) {
		out, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatOn(&cicd), WorkflowName: "release"})
		require.NoError(t, err, "cicd's tree is valid; the laptop's broken child must not be what validates")
		wf, err := wfyaml.ParseWorkflow(out.YAML)
		require.NoError(t, err)
		assert.Contains(t, string(out.YAML), "description: cicd", "the root is cicd's version, not the union's newest")
		assert.Equal(t, "release", wf.GetName())

		wfCtx, err := activity.resolveWorkflowContext(ctx, chatOn(&cicd))
		require.NoError(t, err)
		child, err := activity.loadWorkflowByName(ctx, "step", wfCtx)
		require.NoError(t, err)
		childYAML, err := wfyaml.MarshalWorkflow(child)
		require.NoError(t, err)
		assert.Contains(t, string(childYAML), "from-cicd")
		params, err := activity.createPresetLoader(ctx, wfCtx)("impl")
		require.NoError(t, err)
		assert.Equal(t, "cicd", params["version"])
	})

	t.Run("a run on the laptop loads the laptop's, and fails on its broken child", func(t *testing.T) {
		_, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatOn(&laptop), WorkflowName: "release"})
		require.Error(t, err)
	})

	t.Run("a machine that has never synced falls back to the union", func(t *testing.T) {
		wfCtx, err := activity.resolveWorkflowContext(ctx, chatOn(&fresh))
		require.NoError(t, err)
		root, err := activity.loadWorkflowByName(ctx, "release", wfCtx)
		require.NoError(t, err)
		rootYAML, err := wfyaml.MarshalWorkflow(root)
		require.NoError(t, err)
		assert.Contains(t, string(rootYAML), "description: laptop", "the union's newest push")
	})
}
