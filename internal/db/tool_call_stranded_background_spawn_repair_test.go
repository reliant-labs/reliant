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

// seedBackgroundSpawnToolCall creates a backgrounded (status 6) spawn
// tool_call, exactly the row shape dispatchSpawnBackground writes at
// dispatch time: status=6 with a handle-text result already present. It
// links to a real workflows row via childWorkflowID so the join
// ListStrandedBackgroundSpawnToolCalls uses has something to match.
func seedBackgroundSpawnToolCall(
	t *testing.T, repo *Repo, ctx context.Context,
	toolCallID, chatID, parentThreadID, childWorkflowID string,
) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, repo.UpsertToolCall(ctx, &ToolCall{
		ID:              toolCallID,
		ChatID:          chatID,
		ThreadID:        &parentThreadID,
		ToolName:        "spawn",
		Status:          core.ToolCallStatusBackgrounded,
		ChildWorkflowID: &childWorkflowID,
		RequestedAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}))
	require.NoError(t, repo.UpsertToolCallResult(ctx, &ToolCallResult{
		ToolCallID: toolCallID,
		Content:    "Spawned as agent_id: " + childWorkflowID + " (status: running)",
		CreatedAt:  now,
		UpdatedAt:  now,
	}))
}

// TestListStrandedBackgroundSpawnToolCalls_FindsUnreportedTerminalChild
// covers spec §11 item 4 (the reconciler-sweep test): a worker dying between
// the child reaching a terminal workflow status and the completion enqueue
// landing must be found -- and only that case, not a live or already-
// reported one.
func TestListStrandedBackgroundSpawnToolCalls_FindsUnreportedTerminalChild(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, childThreadID := seedAgentMessageThreads(t, repo, ctx)

	// The bug this repairs, exactly: terminal child, backgrounded call,
	// nothing in the mailbox.
	insertTestWorkflowWithParent(t, repo, childThreadID, chatID, nil, WorkflowStatusCompleted)
	seedBackgroundSpawnToolCall(t, repo, ctx, "tc-bg-stranded", chatID, parentThreadID, childThreadID)

	stranded, err := repo.ListStrandedBackgroundSpawnToolCalls(ctx)
	require.NoError(t, err)

	var found *StrandedBackgroundSpawn
	for _, s := range stranded {
		if s.ToolCallID == "tc-bg-stranded" {
			found = s
		}
	}
	require.NotNil(t, found, "the stranded backgrounded spawn must be found")
	require.Equal(t, chatID, found.ChatID)
	require.NotNil(t, found.ParentThreadID)
	require.Equal(t, parentThreadID, *found.ParentThreadID)
	require.Equal(t, childThreadID, found.ChildThreadID)
	require.Equal(t, WorkflowStatusCompleted, found.WorkflowStatus)
}

// TestListStrandedBackgroundSpawnToolCalls_FailClosed pins spec §11 item 5:
// a still-running (2) or paused (6) child must never be returned, because
// fabricating a completion for a live spawn writes a lie into the parent's
// mailbox that no later pass can distinguish from a real one.
func TestListStrandedBackgroundSpawnToolCalls_FailClosed(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, _ := seedAgentMessageThreads(t, repo, ctx)

	runningChild := uuid.New().String()
	_, err := repo.CreateThread(ctx, &Thread{ID: runningChild, ChatID: chatID, ParentThreadID: &parentThreadID, CreatedAt: time.Now().UTC()})
	require.NoError(t, err)
	insertTestWorkflowWithParent(t, repo, runningChild, chatID, nil, WorkflowStatusRunning)
	seedBackgroundSpawnToolCall(t, repo, ctx, "tc-bg-running", chatID, parentThreadID, runningChild)

	pausedChild := uuid.New().String()
	_, err = repo.CreateThread(ctx, &Thread{ID: pausedChild, ChatID: chatID, ParentThreadID: &parentThreadID, CreatedAt: time.Now().UTC()})
	require.NoError(t, err)
	insertTestWorkflowWithParent(t, repo, pausedChild, chatID, nil, WorkflowStatusPaused)
	seedBackgroundSpawnToolCall(t, repo, ctx, "tc-bg-paused", chatID, parentThreadID, pausedChild)

	// A terminal child that WAS already reported -- a kind=2 completion row
	// exists for its tool_call_id. Must not be re-reported.
	reportedChild := uuid.New().String()
	_, err = repo.CreateThread(ctx, &Thread{ID: reportedChild, ChatID: chatID, ParentThreadID: &parentThreadID, CreatedAt: time.Now().UTC()})
	require.NoError(t, err)
	insertTestWorkflowWithParent(t, repo, reportedChild, chatID, nil, WorkflowStatusCompleted)
	seedBackgroundSpawnToolCall(t, repo, ctx, "tc-bg-reported", chatID, parentThreadID, reportedChild)
	toolCallID := "tc-bg-reported"
	require.NoError(t, repo.EnqueueAgentMessage(ctx, &AgentMessage{
		ID: uuid.New().String(), ChatID: chatID, FromThreadID: reportedChild, ToThreadID: parentThreadID,
		Kind: core.AgentMessageKindCompletion, Body: "already told you", ToolCallID: &toolCallID,
		Status: core.AgentMessageStatusQueued, CreatedAt: time.Now().UTC(),
	}))

	stranded, err := repo.ListStrandedBackgroundSpawnToolCalls(ctx)
	require.NoError(t, err)

	got := map[string]bool{}
	for _, s := range stranded {
		got[s.ToolCallID] = true
	}
	for _, notWant := range []struct {
		id, why string
	}{
		{"tc-bg-running", "a running child is live work; fabricating its completion writes a lie the model reads as fact"},
		{"tc-bg-paused", "paused is resumable — the spawn has not ended, so it has not failed to report"},
		{"tc-bg-reported", "this call already has a real terminal report; a repair would double-deliver it"},
	} {
		if got[notWant.id] {
			t.Errorf("%s returned — %s", notWant.id, notWant.why)
		}
	}
}

// TestEnqueueAgentMessageIfAbsent_IdempotentUnderUniqueConstraint pins spec
// §11's idempotency requirement: two attempts to enqueue a terminal report
// for the same tool_call_id must produce exactly one row, with the second
// call reporting inserted=false rather than erroring -- the everyday
// "someone already reported this" outcome under concurrency, not a failure.
func TestEnqueueAgentMessageIfAbsent_IdempotentUnderUniqueConstraint(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, childThreadID := seedAgentMessageThreads(t, repo, ctx)
	toolCallID := "tc-idempotent"

	first := &AgentMessage{
		ID: uuid.New().String(), ChatID: chatID, FromThreadID: childThreadID, ToThreadID: parentThreadID,
		Kind: core.AgentMessageKindCompletion, Body: "first report", ToolCallID: &toolCallID,
		Status: core.AgentMessageStatusQueued, CreatedAt: time.Now().UTC(),
	}
	inserted, err := repo.EnqueueAgentMessageIfAbsent(ctx, first)
	require.NoError(t, err)
	require.True(t, inserted, "the first enqueue for a tool_call_id must land")

	second := &AgentMessage{
		ID: uuid.New().String(), ChatID: chatID, FromThreadID: childThreadID, ToThreadID: parentThreadID,
		Kind: core.AgentMessageKindCompletion, Body: "second report — must not land", ToolCallID: &toolCallID,
		Status: core.AgentMessageStatusQueued, CreatedAt: time.Now().UTC(),
	}
	inserted, err = repo.EnqueueAgentMessageIfAbsent(ctx, second)
	require.NoError(t, err, "a conflicting insert must be a no-op, not an error")
	require.False(t, inserted, "a second terminal report for the same tool_call_id must not land")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, parentThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1, "exactly one row must exist for this tool_call_id")
	require.Equal(t, "first report", queued[0].Body)

	// A DIFFERENT terminal kind for the same tool_call_id (e.g. failed after
	// completed was already recorded) must also be rejected -- the
	// constraint is per tool_call_id, not per (tool_call_id, kind).
	third := &AgentMessage{
		ID: uuid.New().String(), ChatID: chatID, FromThreadID: childThreadID, ToThreadID: parentThreadID,
		Kind: core.AgentMessageKindFailed, Body: "conflicting kind — must not land", ToolCallID: &toolCallID,
		Status: core.AgentMessageStatusQueued, CreatedAt: time.Now().UTC(),
	}
	inserted, err = repo.EnqueueAgentMessageIfAbsent(ctx, third)
	require.NoError(t, err)
	require.False(t, inserted)
}
