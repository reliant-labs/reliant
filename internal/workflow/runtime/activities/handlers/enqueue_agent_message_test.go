// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnqueueAgentMessage_Completion is the path a detached (background=true)
// spawn uses to notify its parent's mailbox once it finishes: the activity
// must write a queued row addressed to the parent thread, which the parent's
// own drain (at its next step boundary) then delivers.
func TestEnqueueAgentMessage_Completion(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	parentThreadID := uuid.New().String()
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: parentThreadID, ChatID: chatID})
	require.NoError(t, err)

	childThreadID := uuid.New().String()
	_, err = h.Repo().CreateThread(ctx, &db.Thread{ID: childThreadID, ChatID: chatID, ParentThreadID: &parentThreadID})
	require.NoError(t, err)

	act := NewEnqueueAgentMessageActivity(h.Repo())

	var out EnqueueAgentMessageOutput
	err = h.ExecuteActivity(act.Execute, EnqueueAgentMessageInput{
		ChatID:       chatID,
		FromThreadID: childThreadID,
		ToThreadID:   parentThreadID,
		Kind:         int32(core.AgentMessageKindCompletion),
		Body:         "task finished: found the bug",
		ToolCallID:   "toolu_123",
	}, &out)
	require.NoError(t, err)
	assert.NotEmpty(t, out.ID)

	queued, err := h.Repo().ListQueuedAgentMessagesForThread(ctx, parentThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	assert.Equal(t, core.AgentMessageKindCompletion, queued[0].Kind)
	assert.Equal(t, "task finished: found the bug", queued[0].Body)
	assert.Equal(t, childThreadID, queued[0].FromThreadID)
	require.NotNil(t, queued[0].ToolCallID)
	assert.Equal(t, "toolu_123", *queued[0].ToolCallID)
}

// TestEnqueueAgentMessage_CompletionReplacesReconcilerStandIn reproduces the
// error a user saw on chat e6c09159 — "failed to enqueue agent message:
// duplicate key value violates unique constraint
// idx_agent_messages_one_terminal_report_per_spawn (SQLSTATE 23505)".
//
// The reconciler had written a stand-in report into the spawn's one
// terminal-report slot while the run was being reset-and-replayed; the
// replayed spawn then finished and this activity, its own report path, hit
// the unique index on every retry and gave up. The sub-agent's result was
// lost. The activity must instead deliver the real report into that slot.
func TestEnqueueAgentMessage_CompletionReplacesReconcilerStandIn(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	parentThreadID := uuid.New().String()
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: parentThreadID, ChatID: chatID})
	require.NoError(t, err)
	childThreadID := uuid.New().String()
	_, err = h.Repo().CreateThread(ctx, &db.Thread{ID: childThreadID, ChatID: chatID, ParentThreadID: &parentThreadID})
	require.NoError(t, err)

	toolCallID := "toolu_standin"
	standInID := uuid.New().String()
	inserted, err := h.Repo().EnqueueAgentMessageIfAbsent(ctx, &db.AgentMessage{
		ID: standInID, ChatID: chatID, FromThreadID: childThreadID, ToThreadID: parentThreadID,
		Kind: core.AgentMessageKindFailed, Body: "this report was never delivered",
		ToolCallID: &toolCallID, Status: core.AgentMessageStatusUndelivered,
	})
	require.NoError(t, err)
	require.True(t, inserted)

	act := NewEnqueueAgentMessageActivity(h.Repo())
	var out EnqueueAgentMessageOutput
	err = h.ExecuteActivity(act.Execute, EnqueueAgentMessageInput{
		ChatID:       chatID,
		FromThreadID: childThreadID,
		ToThreadID:   parentThreadID,
		Kind:         int32(core.AgentMessageKindCompletion),
		Body:         "the real result",
		ToolCallID:   toolCallID,
	}, &out)
	require.NoError(t, err)
	assert.Equal(t, standInID, out.ID, "the report now lives in the stand-in's row")

	queued, err := h.Repo().ListQueuedAgentMessagesForThread(ctx, parentThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	assert.Equal(t, "the real result", queued[0].Body)
	assert.Equal(t, core.AgentMessageKindCompletion, queued[0].Kind)

	// A retry of the same activity (or a replay re-executing it) is a no-op
	// success, not a second SQLSTATE 23505.
	err = h.ExecuteActivity(act.Execute, EnqueueAgentMessageInput{
		ChatID:       chatID,
		FromThreadID: childThreadID,
		ToThreadID:   parentThreadID,
		Kind:         int32(core.AgentMessageKindCompletion),
		Body:         "the real result",
		ToolCallID:   toolCallID,
	}, &out)
	require.NoError(t, err)
	queued, err = h.Repo().ListQueuedAgentMessagesForThread(ctx, parentThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
}

// TestEnqueueAgentMessage_RequiresToThreadID pins the fail-fast validation:
// no silent orphaned row addressed to nothing.
func TestEnqueueAgentMessage_RequiresToThreadID(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	act := NewEnqueueAgentMessageActivity(h.Repo())
	var out EnqueueAgentMessageOutput
	err := h.ExecuteActivity(act.Execute, EnqueueAgentMessageInput{
		FromThreadID: "from",
		Kind:         int32(core.AgentMessageKindCompletion),
		Body:         "x",
	}, &out)
	require.Error(t, err)
}
