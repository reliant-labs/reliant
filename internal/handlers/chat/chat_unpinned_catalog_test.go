// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// A chat with no pin yet is placed on its machine before a restart's inputs
// are built: the inputs come from that machine's catalog, the one the run's
// loads resolve against once it is pinned. Built first, they came from the
// union, so a laptop's newer schema decided the defaults (or rejected the
// inputs) of a run that then executed cicd's definition.
func TestInputsAfterClosedRunPinsAnUnpinnedChatBeforeBuilding(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	now := time.Now().UTC()
	userID := "user-" + uuid.NewString()
	projectID := "project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "labs", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	cicd, laptop := uuid.NewString(), uuid.NewString()
	for _, id := range []string{cicd, laptop} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: userID}))
	}
	deploy := func(inputs string) *string {
		wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "deploy", Name: "deploy", RelativePath: ".reliant/workflows/deploy.yaml",
			YAMLContent: "name: deploy\napiVersion: v2\ninputs:\n" + inputs + "entry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"}})
		require.NoError(t, err)
		return wj
	}
	// The laptop pushed last, so the union is its definition.
	for daemonID, push := range map[string]struct {
		at     time.Time
		inputs string
	}{
		cicd:   {now.Add(-time.Hour), "  env: {type: string, default: prod}\n"},
		laptop: {now, "  env: {type: string, default: staging}\n  region: {type: string, default: eu}\n"},
	} {
		_, err := repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: projectID, DaemonID: daemonID, ProjectWorkflowsJSON: deploy(push.inputs), PushedAt: push.at,
		}, true)
		require.NoError(t, err)
	}

	// The chat's branch is checked out on cicd; the chat itself has no pin.
	worktreeID := uuid.NewString()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: worktreeID, Name: "feature", Path: t.TempDir(), Branch: "feature", BaseBranch: "main", ProjectID: projectID,
		Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), DaemonID: &cicd, CreatedAt: now, UpdatedAt: now,
	}))
	workflowName := "deploy"
	chat := &db.Chat{
		ID: uuid.NewString(), UserID: userID, ProjectID: projectID, Title: "run", State: db.ChatStateIdle,
		WorktreeID: &worktreeID, WorkflowName: &workflowName, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, repo.CreateChat(ctx, chat))

	svc := &Service{deps: Deps{Repo: repo}}
	inputs, err := svc.inputsAfterClosedRun(ctx, chat, workflowName)
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID, "the chat is pinned before its inputs are built")
	assert.Equal(t, cicd, *chat.ActiveDaemonID)
	assert.Equal(t, "prod", inputs["env"], "cicd's default, not the union's (the laptop's)")
	assert.NotContains(t, inputs, "region", "the laptop's input is not cicd's")
	stored, err := repo.GetChat(ctx, chat.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.ActiveDaemonID)
	assert.Equal(t, cicd, *stored.ActiveDaemonID)
}
