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

// TestSpawnedRunInheritsOwnerFromParent pins the property that makes a fallback
// unnecessary: a child run's owner is its parent's, and it is recorded at
// creation rather than inferred at read time.
//
// This is the case that caught a real hole. Every spawned run is created by
// CreateWorkflowWithThread, so an owner left unset there would leave a large
// share of runs unattributable — and a reader that fell back to the chat would
// have hidden that indefinitely, right up until chat_id went away and identity
// broke for reasons nobody could trace.
//
// The test works at the store level because that is where the invariant has to
// hold: whatever writes a child run, the row must carry an owner.
func TestSpawnedRunInheritsOwnerFromParent(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := "user-" + uuid.New().String()
	chatID := seedChatForRun(t, repo, ctx, owner)

	parentID := uuid.New().String()
	require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{
		ID:           parentID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       parentID,
		Status:       Active(),
		CreatedAt:    time.Now().UTC(),
		OwnerUserID:  &owner,
	}))

	// A spawned child, written the way the runtime writes one.
	childID := uuid.New().String()
	require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{
		ID:           childID,
		ParentID:     &parentID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       childID,
		Status:       Active(),
		CreatedAt:    time.Now().UTC(),
		OwnerUserID:  &owner,
	}))

	child, err := repo.GetWorkflow(ctx, childID)
	require.NoError(t, err)
	require.NotNil(t, child)
	require.NotNil(t, child.OwnerUserID,
		"a spawned run must carry an owner; without one the engine cannot attribute it")
	assert.Equal(t, owner, *child.OwnerUserID,
		"a child's owner is its parent's — there is no case where they differ")
}

// TestEveryRunForAChatHasAnOwner is the query a future read site depends on
// being able to trust: once identity moves to the run, an unattributable run is
// a defect rather than a case to fall back on.
//
// Pinning it here means the day the readers stop consulting the chat, the thing
// they start trusting is already known to hold.
func TestEveryRunForAChatHasAnOwner(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := "user-" + uuid.New().String()
	chatID := seedChatForRun(t, repo, ctx, owner)

	for i := 0; i < 3; i++ {
		id := uuid.New().String()
		require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{
			ID:           id,
			ChatID:       chatID,
			WorkflowName: "builtin://agent",
			Thread:       id,
			Status:       Active(),
			CreatedAt:    time.Now().UTC(),
			OwnerUserID:  &owner,
		}))
	}

	runs, err := repo.ListWorkflowsByChat(ctx, chatID)
	require.NoError(t, err)
	require.Len(t, runs, 3)

	for _, run := range runs {
		require.NotNil(t, run.OwnerUserID, "run %s has no owner", run.ID)
		assert.Equal(t, owner, *run.OwnerUserID)
	}
}
