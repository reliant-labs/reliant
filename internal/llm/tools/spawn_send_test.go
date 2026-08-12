// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

// seedSpawnRelationship creates a parent thread, a child thread it spawned
// (with a matching tool_calls row and a workflows row), and returns their
// ids. childThreadStatus controls whether the child looks alive or finished.
func seedSpawnRelationship(
	t *testing.T,
	repo db.Repository,
	ctx context.Context,
	chatID string,
	childThreadStatus int32,
) (parentThreadID, childThreadID, toolCallID string) {
	t.Helper()
	now := time.Now()

	parentThreadID = uuid.New().String()
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: parentThreadID, ChatID: chatID, Origin: db.ThreadOriginMain,
		Status: db.ThreadStatusRunning, CreatedAt: now,
	})
	require.NoError(t, err)

	childThreadID = uuid.New().String()
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: childThreadID, ChatID: chatID, ParentThreadID: &parentThreadID,
		Origin: db.ThreadOriginSpawn, Status: childThreadStatus, CreatedAt: now,
	})
	require.NoError(t, err)

	childWorkflowID := childThreadID
	wfStatus := db.WorkflowStatusRunning
	if childThreadStatus != db.ThreadStatusRunning {
		wfStatus = db.WorkflowStatusCompleted
	}
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: childWorkflowID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: childThreadID, Status: wfStatus, CreatedAt: now,
	}))

	toolCallID = "toolu_" + uuid.New().String()
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, ThreadID: &parentThreadID,
		ToolName: "spawn", Status: core.ToolCallStatusExecuting,
		ChildWorkflowID: &childWorkflowID,
		RequestedAt:     now, CreatedAt: now, UpdatedAt: now,
	}))

	return parentThreadID, childThreadID, toolCallID
}

func runSpawnSend(t *testing.T, tool Tool, rc *rctx.ToolContext, params SpawnSendParams) ToolResponse {
	t.Helper()
	inputJSON, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(rc, ToolCall{ID: "test-call", Name: SpawnSendToolName, Input: string(inputJSON)})
	require.NoError(t, err)
	return resp
}

func TestSpawnSend_ToRunningChild_QueuesHonestReceipt(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	tool := NewSpawnSendTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnSend(t, tool, rc, SpawnSendParams{AgentID: childID, Message: "status check"})
	require.False(t, resp.IsError, "response: %+v", resp)
	require.Contains(t, resp.Content, "Queued for delivery")
	require.Contains(t, resp.Content, "It has NOT been read yet")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, childID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.Equal(t, "status check", queued[0].Body)
	require.Equal(t, parentID, queued[0].FromThreadID)
}

// TestSpawnSend_ToFinishedAgent_FailsWithGuidance is the spec §7.4
// regression: messaging a finished agent must fail loudly with a pointer to
// spawn(agent_id=...), never silently no-op or resurrect the thread.
func TestSpawnSend_ToFinishedAgent_FailsWithGuidance(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusCompleted)

	tool := NewSpawnSendTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnSend(t, tool, rc, SpawnSendParams{AgentID: childID, Message: "are you still there?"})
	require.True(t, resp.IsError, "sending to a finished agent must fail, not silently succeed")
	require.Contains(t, resp.Content, "spawn(agent_id=")
	require.Contains(t, resp.Content, "already finished")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, childID)
	require.NoError(t, err)
	require.Empty(t, queued, "no message may be enqueued for a finished agent — a silent no-op is exactly what the spec forbids")
}

func TestSpawnSend_ToSibling_Rejected(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, _, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)
	// A second, unrelated thread — not a child of parentID.
	otherID := uuid.New().String()
	now := time.Now()
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: otherID, ChatID: chatID, Origin: db.ThreadOriginMain,
		Status: db.ThreadStatusRunning, CreatedAt: now,
	})
	require.NoError(t, err)

	tool := NewSpawnSendTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnSend(t, tool, rc, SpawnSendParams{AgentID: otherID, Message: "hello"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "parent")
}

// A sub-agent messaging its own parent is the reverse relationship spawn_send
// must also allow per spec §4.4/§4.3.
func TestSpawnSend_ChildToParent_Allowed(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	tool := NewSpawnSendTool(repo)
	// Called FROM the child thread, addressed TO the parent.
	rc := rctx.NewToolContext(ctx, chatID, childID, nil, nil)

	resp := runSpawnSend(t, tool, rc, SpawnSendParams{AgentID: parentID, Message: "I need clarification"})
	require.False(t, resp.IsError, "response: %+v", resp)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.Equal(t, childID, queued[0].FromThreadID)
}
