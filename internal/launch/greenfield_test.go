// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/projectaccess"

	"github.com/reliant-labs/reliant/internal/store"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
)

// greenfieldFixture builds a user, project and chat row for the gating tests.
func greenfieldFixture(t *testing.T) (*db.Repo, context.Context, *db.Chat) {
	t.Helper()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	userID := "greenfield-user"
	// No auth-carrying context: launch takes the owner explicitly, which is
	// what lets it run on the worker.
	ctx := context.Background()

	projectID := "greenfield-project-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Greenfield Project",
		Path:       t.TempDir(),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	chatID := uuid.NewString()
	workflowName := "chat"
	chat := &db.Chat{
		ID:           chatID,
		UserID:       userID,
		ProjectID:    projectID,
		WorkflowName: &workflowName,
		State:        db.ChatStateIdle,
		CreatedAt:    now,
		UpdatedAt:    now,
		LastActive:   now,
	}
	require.NoError(t, repo.CreateChat(ctx, chat))

	return repo, ctx, chat
}

// A chat with no messages yet is on its first turn, which is the only turn the
// run should probe for.
func TestWantsGreenfieldProbeOnAChatsFirstTurn(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	launcher := New(Deps{Store: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), Projects: projectaccess.NewFromORM(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}), Temporal: nil, Runs: nil, TaskQueue: ""})

	assert.True(t, launcher.WantsGreenfieldProbe(ctx, chat))
}

// The guidance is about how to start. Once the conversation is underway the
// user's own words are better evidence than a directory listing, and repeating
// it every turn would be noise.
func TestWantsGreenfieldProbeSkipsAfterFirstTurn(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	launcher := New(Deps{Store: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), Projects: projectaccess.NewFromORM(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}), Temporal: nil, Runs: nil, TaskQueue: ""})

	// A prior turn exists. The root workflow and thread have to exist first —
	// messages hang off a context window, which is scoped to a real thread.
	now := time.Now().UTC()
	_, _, _, err := threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}).CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID:           chat.ID,
			ChatID:       chat.ID,
			WorkflowName: "builtin://agent",
			Thread:       chat.ID,
			Status:       db.Pending(),
			CreatedAt:    now,
		},
		ThreadID: chat.ID,
		ChatID:   chat.ID,
	})
	require.NoError(t, err)
	_, err = repo.SaveMessageToThread(ctx, chat.ID, chat.ID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "build me something", nil, nil, nil)
	require.NoError(t, err)

	assert.False(t, launcher.WantsGreenfieldProbe(ctx, chat),
		"the probe must only run on the first turn of a chat")
}

// A chat with no machine has no directory, and must never reach a daemon.
func TestWantsGreenfieldProbeSkipsANoMachineChat(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	launcher := New(Deps{Store: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), Projects: projectaccess.NewFromORM(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}), Temporal: nil, Runs: nil, TaskQueue: ""})
	chat.NoMachine = true

	assert.False(t, launcher.WantsGreenfieldProbe(ctx, chat))
}
