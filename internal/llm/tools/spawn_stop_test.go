// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/threadcancel"
	"github.com/stretchr/testify/require"
)

// recordingSpawnStopper captures stop requests, and can be made to fail so the
// "could not deliver" path is exercised rather than assumed.
type recordingSpawnStopper struct {
	stops []struct {
		chatID string
		ref    threadcancel.SpawnRef
	}
	err error
}

func (s *recordingSpawnStopper) StopSpawn(_ context.Context, chatID string, ref threadcancel.SpawnRef) error {
	s.stops = append(s.stops, struct {
		chatID string
		ref    threadcancel.SpawnRef
	}{chatID, ref})
	return s.err
}

func runSpawnStop(t *testing.T, tool Tool, rc *rctx.ToolContext, params SpawnStopParams) ToolResponse {
	t.Helper()
	inputJSON, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(rc, ToolCall{ID: "test-call", Name: SpawnStopToolName, Input: string(inputJSON)})
	require.NoError(t, err)
	return resp
}

// The happy path: a direct child is stopped, and the stopper is handed BOTH
// ids. Sending only one is the bug the signal contract exists to prevent —
// child_workflow_id is not guaranteed to equal the child thread id, so the
// receiver may only recognize the spawn by the call that started it.
func TestSpawnStop_DirectChild_RequestsStopWithBothIDs(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, toolCallID := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	stopper := &recordingSpawnStopper{}
	tool := NewSpawnStopTool(repo, stopper)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnStop(t, tool, rc, SpawnStopParams{AgentID: childID, Reason: "fan-out already answered"})
	require.False(t, resp.IsError, "response: %+v", resp)

	require.Len(t, stopper.stops, 1)
	require.Equal(t, chatID, stopper.stops[0].chatID,
		"the stop is addressed to the CHAT's root workflow — a spawn has no execution of its own")
	require.Equal(t, childID, stopper.stops[0].ref.ThreadID)
	require.Equal(t, toolCallID, stopper.stops[0].ref.ToolCallID,
		"both ids must travel: the receiver may only know the spawn by the call that started it")
}

// seedResumption adds a SECOND spawn tool call for an existing child thread —
// what `spawn(agent_id=...)` produces. The thread stays the original, while the
// executing workflows row is a fresh id derived from the new tool call, so the
// workflow row id differs from the thread id.
func seedResumption(
	t *testing.T, repo db.Repository, ctx context.Context,
	chatID, parentThreadID, childThreadID string, requestedAt time.Time,
) (toolCallID, workflowID string) {
	t.Helper()
	toolCallID = "toolu_" + uuid.New().String()
	workflowID = "wf-" + uuid.New().String()

	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: workflowID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: childThreadID, Status: db.Active(), CreatedAt: requestedAt,
	}))
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, ThreadID: &parentThreadID,
		ToolName: "spawn", Status: core.ToolCallStatusExecuting,
		ChildWorkflowID: &workflowID,
		RequestedAt:     requestedAt, CreatedAt: requestedAt, UpdatedAt: requestedAt,
	}))
	return toolCallID, workflowID
}

// A RESUMED spawn yields one spawn_children row per resumption for the same
// child thread, oldest first. spawn_stop must address the LATEST: taking the
// first returned the oldest tool call and the oldest — already finished —
// workflow row, so the live row stayed active after a stop that reported
// success. The signal alone still landed (the runtime also matches thread id),
// which is exactly why this was easy to miss.
func TestSpawnStop_ResumedSpawn_TargetsLatestResumption(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	// The original spawn: its workflow row is already finished.
	parentID, childID, originalToolCallID := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)
	originalWorkflowID := childID // a first spawn: workflow id == thread id

	// A later resumption of the same thread, with its own call and workflow row.
	resumedToolCallID, resumedWorkflowID := seedResumption(
		t, repo, ctx, chatID, parentID, childID, time.Now().Add(time.Minute))
	require.NotEqual(t, originalToolCallID, resumedToolCallID)
	require.NotEqual(t, childID, resumedWorkflowID,
		"the whole bug depends on the resumed workflow id differing from the thread id")

	stopper := &recordingSpawnStopper{}
	tool := NewSpawnStopTool(repo, stopper)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnStop(t, tool, rc, SpawnStopParams{AgentID: childID})
	require.False(t, resp.IsError, "response: %+v", resp)

	require.Len(t, stopper.stops, 1)
	ref := stopper.stops[0].ref
	require.Equal(t, childID, ref.ThreadID, "the thread stays the original across resumptions")
	require.Equal(t, resumedToolCallID, ref.ToolCallID,
		"must be the LATEST tool call, not the oldest (%s)", originalToolCallID)
	require.Equal(t, resumedWorkflowID, ref.WorkflowID,
		"must be the LATEST workflow row, not the original (%s)", originalWorkflowID)
}

// The receipt must not overstate what happened. A model that reads "stopped"
// will treat the delegated work as abandoned, when in fact an agent inside a
// long tool call keeps running until that call returns.
func TestSpawnStop_Receipt_SaysRequestedNotStopped(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	tool := NewSpawnStopTool(repo, &recordingSpawnStopper{})
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnStop(t, tool, rc, SpawnStopParams{AgentID: childID})
	require.False(t, resp.IsError, "response: %+v", resp)
	require.Contains(t, resp.Content, "REQUESTED")
	require.Contains(t, resp.Content, "next step boundary")
	require.Contains(t, resp.Content, "spawn_status(agent_id=",
		"the receipt must name the one call that confirms the stop actually landed")
}

// Stopping your own parent would end the run that is waiting on you. This is
// stricter than spawn_send, which allows child->parent messages on purpose.
func TestSpawnStop_Parent_Rejected(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	stopper := &recordingSpawnStopper{}
	tool := NewSpawnStopTool(repo, stopper)
	// The CHILD is the caller here, trying to stop its parent.
	rc := rctx.NewToolContext(ctx, chatID, childID, nil, nil)

	resp := runSpawnStop(t, tool, rc, SpawnStopParams{AgentID: parentID})
	require.True(t, resp.IsError, "a child must not be able to stop its parent")
	require.Contains(t, resp.Content, "not a sub-agent you spawned")
	require.Empty(t, stopper.stops, "a refused stop must not reach the workflow")
}

func TestSpawnStop_UnrelatedThread_Rejected(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, _, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)
	// A second thread in the same chat that parentID did not spawn.
	otherID := uuid.New().String()
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: otherID, ChatID: chatID, Origin: db.ThreadOriginMain,
		Status: db.ThreadStatusRunning, CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	stopper := &recordingSpawnStopper{}
	tool := NewSpawnStopTool(repo, stopper)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnStop(t, tool, rc, SpawnStopParams{AgentID: otherID})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "not a sub-agent you spawned")
	require.Empty(t, stopper.stops)
}

// An agent that already finished is not an error: nothing is wrong, there is
// simply no loop left to stop. Reporting it as a failure would push a model to
// retry or to treat the agent's real result as suspect.
func TestSpawnStop_AlreadyFinished_ReportsWithoutError(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusCompleted)

	stopper := &recordingSpawnStopper{}
	tool := NewSpawnStopTool(repo, stopper)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnStop(t, tool, rc, SpawnStopParams{AgentID: childID})
	require.False(t, resp.IsError, "a finished agent is not a failure to report: %+v", resp)
	require.Contains(t, resp.Content, "already finished")
	require.Empty(t, stopper.stops, "there is no running loop to signal")
}

// No stopper means no way to stop anything — the daemon runtime builds a tools
// factory with no Temporal connection. Unlike spawn_send's doorbell there is no
// degraded fallback: an undelivered stop stops nothing, so the tool must say the
// agent is still running rather than claim a cancellation.
func TestSpawnStop_NilStopper_ReportsUnavailable(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	tool := NewSpawnStopTool(repo, nil)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnStop(t, tool, rc, SpawnStopParams{AgentID: childID})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "not available in this runtime")
	require.Contains(t, resp.Content, "still running")
}

// A delivery failure means the agent is STILL RUNNING. Saying anything else is
// the exact failure mode this tool's receipt exists to avoid.
func TestSpawnStop_DeliveryFails_SaysStillRunning(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	stopper := &recordingSpawnStopper{err: errors.New("workflow not found")}
	tool := NewSpawnStopTool(repo, stopper)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnStop(t, tool, rc, SpawnStopParams{AgentID: childID})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "STILL RUNNING")
	require.Contains(t, resp.Content, "workflow not found")
}

func TestSpawnStop_SelfRejected(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, _, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	stopper := &recordingSpawnStopper{}
	tool := NewSpawnStopTool(repo, stopper)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp := runSpawnStop(t, tool, rc, SpawnStopParams{AgentID: parentID})
	require.True(t, resp.IsError)
	require.Empty(t, stopper.stops)
}
