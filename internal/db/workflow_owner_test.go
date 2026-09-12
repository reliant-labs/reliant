// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedChatForRun creates the chat a run hangs off, and returns its id.
func seedChatForRun(t *testing.T, repo *Repo, ctx context.Context, userID string) string {
	t.Helper()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "run owner", ProjectID: "test-project", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return chatID
}

// TestWorkflowOwnerUserID_RoundTrips pins that a run can carry its own identity.
//
// A run has to know who it belongs to. Today every activity needing an identity
// re-reads the chat and takes user_id off it, which works only because
// workflows.chat_id is NOT NULL — the coupling the engine split removes. A
// triggered or API-started run has no conversation to borrow from.
//
// Nothing reads this column yet; the readers flip in a follow-up with a
// fallback to the chat. What this protects is that the value survives the write
// path at all, because a column that silently dropped its value would make that
// flip look correct while quietly losing identity.
func TestWorkflowOwnerUserID_RoundTrips(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := "user-" + uuid.New().String()
	chatID := seedChatForRun(t, repo, ctx, owner)

	runID := uuid.New().String()
	require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{
		ID:           runID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       runID,
		Status:       Pending(),
		CreatedAt:    time.Now().UTC(),
		OwnerUserID:  &owner,
	}))

	got, err := repo.GetWorkflow(ctx, runID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.OwnerUserID, "the run lost its owner on the way through the store")
	assert.Equal(t, owner, *got.OwnerUserID)
}

// TestWorkflowOwnerUserID_NilIsAllowed covers the rows this column legitimately
// has none for: written before the migration, or belonging to a deleted chat.
//
// NULL has to be a normal value rather than an error. Readers fall back to the
// chat while both sources exist, so an unknown owner costs nothing today — and
// rejecting it would make the migration unable to run against real data.
func TestWorkflowOwnerUserID_NilIsAllowed(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := seedChatForRun(t, repo, ctx, "user-"+uuid.New().String())

	runID := uuid.New().String()
	require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{
		ID:           runID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       runID,
		Status:       Pending(),
		CreatedAt:    time.Now().UTC(),
		// OwnerUserID deliberately unset.
	}))

	got, err := repo.GetWorkflow(ctx, runID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.OwnerUserID, "an unset owner must read back as nil, not empty-string")
}
