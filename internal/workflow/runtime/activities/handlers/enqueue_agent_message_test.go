// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

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

const lostInTransitBody = "Sub-agent finished while its result was lost in transit; check spawn_status."

type spawnReportFixture struct {
	h              *IdempotencyTestHelper
	chatID         string
	parentThreadID string
	childThreadID  string
}

func newSpawnReportFixture(t *testing.T) *spawnReportFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()
	userID, projectID, chatID := uuid.New().String(), uuid.New().String(), uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	parent, child := uuid.New().String(), uuid.New().String()
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: parent, ChatID: chatID})
	require.NoError(t, err)
	_, err = h.Repo().CreateThread(ctx, &db.Thread{ID: child, ChatID: chatID, ParentThreadID: &parent})
	require.NoError(t, err)
	return &spawnReportFixture{h: h, chatID: chatID, parentThreadID: parent, childThreadID: child}
}

func (f *spawnReportFixture) seedPlaceholder(t *testing.T, toolCallID string) *core.AgentMessage {
	t.Helper()
	placeholder := &core.AgentMessage{
		ID: uuid.New().String(), ChatID: f.chatID, FromThreadID: f.childThreadID, ToThreadID: f.parentThreadID,
		Kind: core.AgentMessageKindCompletion, Body: lostInTransitBody, ToolCallID: &toolCallID,
		Status: core.AgentMessageStatusQueued, CreatedAt: time.Now().UTC(), Synthesized: true,
	}
	inserted, err := f.h.Repo().EnqueueAgentMessageIfAbsent(context.Background(), placeholder)
	require.NoError(t, err)
	require.True(t, inserted)
	return placeholder
}

func (f *spawnReportFixture) report(t *testing.T, kind core.AgentMessageKind, toolCallID, body string) (EnqueueAgentMessageOutput, error) {
	t.Helper()
	act := NewEnqueueAgentMessageActivity(f.h.Repo())
	var out EnqueueAgentMessageOutput
	err := f.h.ExecuteActivity(act.Execute, EnqueueAgentMessageInput{
		ChatID: f.chatID, FromThreadID: f.childThreadID, ToThreadID: f.parentThreadID,
		Kind: int32(kind), Body: body, ToolCallID: toolCallID,
	}, &out)
	return out, err
}

func (f *spawnReportFixture) rowsFor(t *testing.T, toolCallID string) []*core.AgentMessage {
	t.Helper()
	var rows []*core.AgentMessage
	// Queued rows only: delivered/undelivered placeholders are asserted via the row ids below.
	queued, err := f.h.Repo().ListQueuedAgentMessagesForThread(context.Background(), f.parentThreadID)
	require.NoError(t, err)
	for _, m := range queued {
		if m.ToolCallID != nil && *m.ToolCallID == toolCallID {
			rows = append(rows, m)
		}
	}
	return rows
}

// A real report arriving after the reconciler synthesized a placeholder must
// replace it, not die on the unique index (incident 2026-10-04).
func TestEnqueueAgentMessage_RealReportSupersedesQueuedPlaceholder(t *testing.T) {
	f := newSpawnReportFixture(t)
	f.seedPlaceholder(t, "toolu_sup")

	out, err := f.report(t, core.AgentMessageKindCompletion, "toolu_sup", "real result: bug found")
	require.NoError(t, err)
	assert.NotEmpty(t, out.ID)

	rows := f.rowsFor(t, "toolu_sup")
	require.Len(t, rows, 1)
	assert.Equal(t, "real result: bug found", rows[0].Body)
	assert.Equal(t, core.AgentMessageStatusQueued, rows[0].Status)
	assert.False(t, rows[0].Synthesized)
}

// A placeholder the parent already consumed is still superseded, and re-queued
// so the parent receives the actual outcome.
func TestEnqueueAgentMessage_RealReportSupersedesDeliveredPlaceholder(t *testing.T) {
	f := newSpawnReportFixture(t)
	placeholder := f.seedPlaceholder(t, "toolu_del")
	moved, err := f.h.Repo().MarkAgentMessagesDelivered(context.Background(), []string{placeholder.ID}, time.Now().UTC(), "")
	require.NoError(t, err)
	require.Len(t, moved, 1)

	_, err = f.report(t, core.AgentMessageKindFailed, "toolu_del", "real failure: boom")
	require.NoError(t, err)

	rows := f.rowsFor(t, "toolu_del")
	require.Len(t, rows, 1)
	assert.Equal(t, "real failure: boom", rows[0].Body)
	assert.Equal(t, core.AgentMessageKindFailed, rows[0].Kind)
	assert.Nil(t, rows[0].DeliveredAt)
	assert.Nil(t, rows[0].DeliveredMessageID)
	assert.False(t, rows[0].Synthesized)
}

// A drain lists its batch outside its transaction and claims it by id. If the
// real report superseded a placeholder in between, the drain still holds the
// placeholder's id and stale body; claiming that id must take NOTHING, or the
// drain would deliver the placeholder text and mark the real report delivered
// without the parent ever seeing it.
func TestEnqueueAgentMessage_SupersedeDefeatsAStaleDrainClaim(t *testing.T) {
	f := newSpawnReportFixture(t)
	ctx := context.Background()
	f.seedPlaceholder(t, "toolu_race")

	listed := f.rowsFor(t, "toolu_race") // the drain's unguarded read
	require.Len(t, listed, 1)
	require.Equal(t, lostInTransitBody, listed[0].Body)

	_, err := f.report(t, core.AgentMessageKindCompletion, "toolu_race", "real result")
	require.NoError(t, err)

	claimed, err := f.h.Repo().MarkAgentMessagesDelivered(ctx, []string{listed[0].ID}, time.Now().UTC(), "")
	require.NoError(t, err)
	assert.Empty(t, claimed, "a claim on the superseded placeholder's id must not take the real report")

	rows := f.rowsFor(t, "toolu_race")
	require.Len(t, rows, 1, "the real report must still be queued for the next drain")
	assert.Equal(t, "real result", rows[0].Body)
	assert.NotEqual(t, listed[0].ID, rows[0].ID)
}

// A second real report (activity retry after a lost commit response) is an
// idempotent no-op and leaves the first report untouched.
func TestEnqueueAgentMessage_AlreadyReportedIsIdempotent(t *testing.T) {
	f := newSpawnReportFixture(t)
	_, err := f.report(t, core.AgentMessageKindCompletion, "toolu_idem", "first")
	require.NoError(t, err)

	_, err = f.report(t, core.AgentMessageKindCompletion, "toolu_idem", "second")
	require.NoError(t, err)

	rows := f.rowsFor(t, "toolu_idem")
	require.Len(t, rows, 1)
	assert.Equal(t, "first", rows[0].Body)
}

// Non-terminal kinds keep the plain insert path.
func TestEnqueueAgentMessage_NonTerminalKindsUnaffected(t *testing.T) {
	f := newSpawnReportFixture(t)
	_, err := f.report(t, core.AgentMessageKindMessage, "", "hello")
	require.NoError(t, err)
	_, err = f.report(t, core.AgentMessageKindHumanMessage, "", "hi")
	require.NoError(t, err)
	queued, err := f.h.Repo().ListQueuedAgentMessagesForThread(context.Background(), f.parentThreadID)
	require.NoError(t, err)
	assert.Len(t, queued, 2)
}
