// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// ListSpawnChildren must carry the workflow row id next to the thread id.
//
// For a RESUMED spawn (spawn with agent_id) they differ: the thread stays the
// original while every resumption derives a fresh workflow row from its new
// tool call. A caller that stops a spawn needs both — the thread for the
// cancel signal, the workflow row for the status reconcile — and cannot derive
// one from the other, so a list that exposed only the thread id left it
// guessing.
func TestListSpawnChildren_CarriesWorkflowIDDistinctFromThreadForResumedSpawn(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	chatID := uuid.New().String()
	parentThreadID := uuid.New().String()
	childThreadID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "resumed spawn", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &Thread{ID: parentThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &Thread{
		ID: childThreadID, ChatID: chatID, ParentThreadID: &parentThreadID,
		Origin: core.ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)

	// Two calls for ONE child thread: the original, then a resumption.
	type call struct{ toolCallID, workflowID string }
	calls := []call{
		{"toolu_first_" + uuid.New().String()[:8], childThreadID},
		{"toolu_resume_" + uuid.New().String()[:8], "wf-resume-" + uuid.New().String()[:8]},
	}
	for i, c := range calls {
		at := now.Add(time.Duration(i) * time.Minute)
		wfID, tcID := c.workflowID, c.toolCallID
		require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{
			ID: wfID, ChatID: chatID, WorkflowName: "builtin://agent",
			Thread: childThreadID, Status: Active(), CreatedAt: at,
		}))
		require.NoError(t, repo.UpsertToolCall(ctx, &ToolCall{
			ID: tcID, ChatID: chatID, ThreadID: &parentThreadID, ToolName: "spawn",
			Status: core.ToolCallStatusExecuting, ChildWorkflowID: &wfID,
			RequestedAt: at, CreatedAt: at, UpdatedAt: at,
		}))
	}

	children, err := repo.ListSpawnChildren(ctx, parentThreadID)
	require.NoError(t, err)
	require.Len(t, children, 2, "one row per resumption")

	for i, child := range children {
		require.NotNil(t, child.ChildThreadID)
		require.Equal(t, childThreadID, *child.ChildThreadID, "the thread is the same across resumptions")
		require.NotNil(t, child.ChildWorkflowID, "the workflow row id must be exposed")
		require.Equal(t, calls[i].workflowID, *child.ChildWorkflowID)
		require.Equal(t, calls[i].toolCallID, child.ToolCallID)
	}
	require.NotEqual(t, *children[1].ChildWorkflowID, *children[1].ChildThreadID,
		"for a resumed spawn the workflow id differs from the thread id — the case that needs both")
}
