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
	wfStatus := db.Active()
	if childThreadStatus != db.ThreadStatusRunning {
		wfStatus = db.Completed()
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

	tool := NewSpawnSendTool(repo, nil)
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

// recordingAgentMessageNotifier captures doorbell rings.
type recordingAgentMessageNotifier struct {
	rings []struct{ chatID, threadID string }
}

func (n *recordingAgentMessageNotifier) NotifyAgentMessageQueued(_ context.Context, chatID, toThreadID string) {
	n.rings = append(n.rings, struct{ chatID, threadID string }{chatID, toThreadID})
}

// TestSpawnSend_ChildToParent_RingsMailboxDoorbell is the agent-to-agent half
// of the mailbox deadlock.
//
// A child reporting up to its parent is the WORST case for this bug, not an
// edge case: the parent is parked in awaitLiveDetachedSpawns precisely
// because this child (and its siblings) are still running, and delivery only
// happens at a loop-step boundary the parked parent never reaches. Queuing
// without a doorbell means the message waits for a SIBLING to finish — and if
// this child is the last one, the parent's gate wakes for the completion,
// exits, and the message is marked undelivered having never been read.
func TestSpawnSend_ChildToParent_RingsMailboxDoorbell(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	notifier := &recordingAgentMessageNotifier{}
	tool := NewSpawnSendTool(repo, notifier)
	// The CHILD is the caller here, messaging its parent.
	rc := rctx.NewToolContext(ctx, chatID, childID, nil, nil)

	resp := runSpawnSend(t, tool, rc, SpawnSendParams{AgentID: parentID, Message: "found the root cause"})
	require.False(t, resp.IsError, "response: %+v", resp)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.Equal(t, childID, queued[0].FromThreadID)

	require.Len(t, notifier.rings, 1,
		"a message to a parent parked on its sub-agents must wake it, not wait for a sibling to finish")
	require.Equal(t, chatID, notifier.rings[0].chatID)
	require.Equal(t, parentID, notifier.rings[0].threadID,
		"the doorbell must name the RECIPIENT thread so the right gate wakes")
}

// A refused send must not ring the doorbell: there is no row to drain, and
// waking a parked parent to find an empty mailbox is a wasted turn.
func TestSpawnSend_RefusedSend_DoesNotRingDoorbell(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusCompleted)

	notifier := &recordingAgentMessageNotifier{}
	tool := NewSpawnSendTool(repo, notifier)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnSend(t, tool, rc, SpawnSendParams{AgentID: childID, Message: "are you there?"})
	require.True(t, resp.IsError)
	require.Empty(t, notifier.rings, "nothing was queued, so there is nothing to wake for")
}

// A nil notifier must not panic. The daemon runtime builds a tools factory
// with no Temporal connection at all, and spawn_send has to keep working
// there — degraded to next-boundary delivery, not broken.
func TestSpawnSend_NilNotifier_StillQueues(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	tool := NewSpawnSendTool(repo, nil)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnSend(t, tool, rc, SpawnSendParams{AgentID: childID, Message: "still queued"})
	require.False(t, resp.IsError, "response: %+v", resp)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, childID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
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

	tool := NewSpawnSendTool(repo, nil)
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

	tool := NewSpawnSendTool(repo, nil)
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

	tool := NewSpawnSendTool(repo, nil)
	// Called FROM the child thread, addressed TO the parent.
	rc := rctx.NewToolContext(ctx, chatID, childID, nil, nil)

	resp := runSpawnSend(t, tool, rc, SpawnSendParams{AgentID: parentID, Message: "I need clarification"})
	require.False(t, resp.IsError, "response: %+v", resp)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.Equal(t, childID, queued[0].FromThreadID)
}
