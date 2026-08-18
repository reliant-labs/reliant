// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

// createTestWorkflowRow inserts a root workflow row for checkpoint tests.
func createTestWorkflowRow(t *testing.T, repo db.Repository, chatID string) string {
	t.Helper()
	ctx := context.Background()
	workflowID := uuid.New().String()
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       workflowID,
		Status:       db.Active(),
		CreatedAt:    time.Now().UTC(),
	}))
	return workflowID
}

func TestWorkflowCheckpointActivity_UpsertAndRead(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID := createTestChat(t, repo)
	workflowID := createTestWorkflowRow(t, repo, chatID)

	act := NewWorkflowCheckpointActivity(repo)

	// First write: node entry.
	out, err := act.Execute(ctx, WorkflowCheckpointInput{
		ChatID:     chatID,
		WorkflowID: workflowID,
		NodeID:     "plan_deck",
	})
	require.NoError(t, err)
	require.True(t, out.Success)

	cp, err := repo.GetWorkflowCheckpoint(ctx, workflowID)
	require.NoError(t, err)
	require.NotNil(t, cp)
	require.Equal(t, "plan_deck", cp.NodeID)
	require.Equal(t, chatID, cp.ChatID)
	require.EqualValues(t, 0, cp.LoopIteration)

	// Second write for the same workflow ID overwrites (one row per run).
	_, err = act.Execute(ctx, WorkflowCheckpointInput{
		ChatID:        chatID,
		WorkflowID:    workflowID,
		NodeID:        "agent_loop",
		LoopIteration: 7,
	})
	require.NoError(t, err)

	cp, err = repo.GetWorkflowCheckpoint(ctx, workflowID)
	require.NoError(t, err)
	require.NotNil(t, cp)
	require.Equal(t, "agent_loop", cp.NodeID)
	require.EqualValues(t, 7, cp.LoopIteration)
}

func TestWorkflowCheckpoint_MissingReturnsNil(t *testing.T) {
	repo := setupTestRepo(t)
	cp, err := repo.GetWorkflowCheckpoint(context.Background(), "no-such-workflow")
	require.NoError(t, err)
	require.Nil(t, cp, "missing checkpoint must be (nil, nil), not an error")
}

// TestWorkflowStatus_CheckpointLifecycle pins the terminal-status contract:
//   - completed  -> checkpoint deleted (next run starts fresh)
//   - cancelled  -> checkpoint deleted (user cancel starts fresh)
//   - failed     -> checkpoint KEPT (next run resumes at position)
func TestWorkflowStatus_CheckpointLifecycle(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	writeCheckpoint := func(t *testing.T, chatID, workflowID string) {
		t.Helper()
		require.NoError(t, repo.UpsertWorkflowCheckpoint(ctx, &db.WorkflowCheckpoint{
			WorkflowID:    workflowID,
			ChatID:        chatID,
			NodeID:        "agent_loop",
			LoopIteration: 3,
		}))
	}

	statusAct := NewWorkflowStatusActivity(repo)

	// One chat shared across subtests (createTestChat's project path is fixed
	// per repo); each subtest gets its own workflow row.
	chatID := createTestChat(t, repo)

	cases := []struct {
		status   string
		wantKept bool
	}{
		{status: "completed", wantKept: false},
		{status: "cancelled", wantKept: false},
		{status: "failed", wantKept: true},
	}

	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			workflowID := createTestWorkflowRow(t, repo, chatID)
			writeCheckpoint(t, chatID, workflowID)

			_, err := statusAct.Execute(ctx, WorkflowStatusInput{
				ChatID:       chatID,
				WorkflowID:   workflowID,
				WorkflowName: "builtin://agent",
				Status:       tc.status,
			})
			require.NoError(t, err)

			cp, err := repo.GetWorkflowCheckpoint(ctx, workflowID)
			require.NoError(t, err)
			if tc.wantKept {
				require.NotNil(t, cp, "failed runs must keep the checkpoint for resume-at-position")
				require.Equal(t, "agent_loop", cp.NodeID)
				require.EqualValues(t, 3, cp.LoopIteration)
			} else {
				require.Nil(t, cp, "%s runs must clear the checkpoint", tc.status)
			}
		})
	}
}
