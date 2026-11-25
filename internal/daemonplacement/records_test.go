// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// Records keeps the *db.Repo reads' empty-id guard: an empty id is refused
// before any query, so a request that names none never reads a row.
func TestRecordsRefuseAnEmptyID(t *testing.T) {
	r := NewRecords(nil)
	ctx := context.Background()
	_, err := r.GetDaemon(ctx, "")
	require.EqualError(t, err, "daemon ID cannot be empty")
	_, err = r.GetWorktree(ctx, "")
	require.EqualError(t, err, "worktree ID cannot be empty")
	_, err = r.GetProject(ctx, "")
	require.EqualError(t, err, "project ID cannot be empty")
	_, err = r.ListProjectDaemonsForProject(ctx, "")
	require.EqualError(t, err, "project ID cannot be empty")
	_, err = r.GetChat(ctx, "")
	require.EqualError(t, err, "chat ID cannot be empty")
}

// Exercise the production chat store, not a fake reader: selecting the wrong
// row must not turn another tenant's chat into a usable machine target.
func TestRecordsWorkspaceDaemonChatOwnership(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, userID := range []string{"owner", "other"} {
		require.NoError(t, repo.CreateProject(ctx, &db.Project{
			ID: "project-" + userID, UserID: userID, Name: userID,
			Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		workflow := "builtin://agent"
		machine := "machine-" + userID
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{
			ID: "chat-" + userID, UserID: userID, ProjectID: "project-" + userID,
			Title: userID, State: db.ChatStateIdle, WorkflowName: &workflow,
			ActiveDaemonID: &machine, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
	}

	records := NewRecords(repo.ORM())
	// Read the foreign chat first so a store that ignores the requested ID
	// cannot accidentally pass by returning the owned row for every request.
	for _, userID := range []string{"other", "owner"} {
		chat, err := records.GetChat(ctx, "chat-"+userID)
		require.NoError(t, err)
		require.NotNil(t, chat)
		require.Equal(t, "chat-"+userID, chat.ID)
		require.Equal(t, userID, chat.UserID)
		require.Equal(t, "project-"+userID, chat.ProjectID)
		require.Equal(t, strp("machine-"+userID), chat.ActiveDaemonID)
	}

	foreign, err := WorkspaceDaemon(ctx, records, "owner", "", "", "chat-other")
	require.ErrorIs(t, err, ErrForeignChat)
	require.Equal(t, Workspace{}, foreign, "denial must not expose a target machine")

	owned, err := WorkspaceDaemon(ctx, records, "owner", "project-owner", "", "chat-owner")
	require.NoError(t, err)
	require.Equal(t, Workspace{DaemonID: "machine-owner"}, owned)
}
