// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An async question (ask_user async: true) is asked without waiting, so it is
// never a gate: not AWAITING_INPUT, not a pending or answerable question of
// the chat, and its thread still works. It is an item in the user's inbox
// until answered, whatever its runs did since, and a thread that finishes
// leaves it pending. research/ASYNC_ASK_USER.md.

// askAsync records a pending async ask_user question from the run rowID on
// thread.
func (c *liveGateChat) askAsync(id, rowID, thread string) {
	c.t.Helper()
	metadata := `{"type":"ask_user","async":true,"questions":[{"question":"` + id + `?"}]}`
	toolCall := "call-" + id
	if err := c.repo.CreateQuestion(context.Background(), &Question{
		ID: id, ChatID: c.chatID, WorkflowID: rowID, TemporalWorkflowID: c.chatID,
		ThreadID: thread, StepID: "agent_loop", Status: QuestionStatusPending,
		Metadata: &metadata, ToolCallID: &toolCall, Async: true, CreatedAt: time.Now(),
	}); err != nil {
		c.t.Fatalf("CreateQuestion(%s): %v", id, err)
	}
}

func (c *liveGateChat) inboxQuestionKeys() map[string]int32 {
	c.t.Helper()
	items, err := c.repo.ListInboxPending(context.Background(), "test-user")
	require.NoError(c.t, err)
	keys := map[string]int32{}
	for _, it := range items {
		if it.ChatID == c.chatID && it.Kind == 2 {
			keys[it.ItemKey] = it.Int1
		}
	}
	return keys
}

func questionIDs(qs []*Question) []string {
	ids := make([]string, 0, len(qs))
	for _, q := range qs {
		ids = append(ids, q.ID)
	}
	return ids
}

func TestAsyncQuestion_IsNoGateOfTheChat(t *testing.T) {
	c := newLiveGateChat(t, "chat-async-no-gate")
	c.spawn("sa-1", Active())
	c.askAsync("q-async", "sa-1", "sa-1")

	// Not AWAITING_INPUT, and the asking thread still counts as working.
	c.expect(activityRunning, 1)

	ctx := context.Background()
	var gates int
	require.NoError(t, c.repo.ORM().Bun().NewRaw(
		`SELECT count(*) FROM pending_input_gates WHERE chat_id = ?`, c.chatID).Scan(ctx, &gates))
	assert.Zero(t, gates, "an async question is not in pending_input_gates")

	answerable, err := c.repo.ListAnswerablePendingQuestionsByChat(ctx, c.chatID)
	require.NoError(t, err)
	assert.Empty(t, answerable, "the composer and the pill never see an async question")
	pending, err := c.repo.ListPendingQuestionsByChat(ctx, c.chatID)
	require.NoError(t, err)
	assert.Empty(t, pending, "nor do the reconciler, terminate or spawn_status")
	byThread, err := c.repo.PendingQuestionsByThread(ctx, c.chatID)
	require.NoError(t, err)
	assert.Empty(t, byThread)

	// A blocking question beside it is a gate as before.
	c.ask("q-blocking", "sa-1", "sa-1")
	c.expect(activityAwaitingInput, 0)
	answerable, err = c.repo.ListAnswerablePendingQuestionsByChat(ctx, c.chatID)
	require.NoError(t, err)
	assert.Equal(t, []string{"q-blocking"}, questionIDs(answerable))

	others, err := c.repo.CountOtherPendingBlockers(ctx, c.chatID, "q-blocking")
	require.NoError(t, err)
	assert.Zero(t, others, "an async question blocks no run")
}

func TestAsyncQuestion_ListedByListAsyncQuestionsByChat(t *testing.T) {
	c := newLiveGateChat(t, "chat-async-list")
	c.spawn("sa-1", Active())
	c.ask("q-blocking", "sa-1", "sa-1")
	c.askAsync("q-async-1", "sa-1", "sa-1")
	time.Sleep(2 * time.Millisecond)
	c.askAsync("q-async-2", c.chatID, c.chatID)
	answer := `{"answers":[{"question":"q-async-1?","selected":["Yes"]}]}`
	require.NoError(t, c.repo.ResolveQuestion(context.Background(), "q-async-1", &answer))

	got, err := c.repo.ListAsyncQuestionsByChat(context.Background(), c.chatID)
	require.NoError(t, err)
	require.Equal(t, []string{"q-async-1", "q-async-2"}, questionIDs(got), "async only, answered too, oldest first")
	assert.True(t, got[0].Async)
	assert.Equal(t, QuestionStatusResolved, got[0].Status)
	require.NotNil(t, got[0].ResponseData)
	assert.Equal(t, answer, *got[0].ResponseData)
	require.NotNil(t, got[1].ToolCallID)
	assert.Equal(t, "call-q-async-2", *got[1].ToolCallID)
}

// The inbox lists an async question whatever its runs did — the asking
// sub-agent finished, the chat's root completed — because its answer is
// delivered as a message, which reaches a finished thread too. A blocking
// question in the same state is dropped, as before.
func TestAsyncQuestion_StaysInTheInboxAfterItsRunsEnd(t *testing.T) {
	c := newLiveGateChat(t, "chat-async-inbox")
	c.spawn("sa-done", Active())
	c.askAsync("q-async", "sa-done", "sa-done")
	c.ask("q-blocking", "sa-done", "sa-done")

	assert.Equal(t, map[string]int32{"q-async": 1, "q-blocking": 0}, c.inboxQuestionKeys(),
		"both listed while the run is live; a_int marks the async one")

	c.setStatus("sa-done", Completed())
	c.setStatus(c.chatID, Completed())
	assert.Equal(t, map[string]int32{"q-async": 1}, c.inboxQuestionKeys(),
		"the async question outlives its runs in the inbox; the blocking one does not")

	require.NoError(t, c.repo.ResolveQuestion(context.Background(), "q-async", nil))
	assert.Empty(t, c.inboxQuestionKeys(), "answered, it leaves the inbox")
}

// A thread that finishes closes its blocking questions (question_close.go)
// and leaves its async ones pending: no wait is behind them to end.
func TestAsyncQuestion_ThreadEndDoesNotCloseIt(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-async-close"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflowWithParent(t, repo, "wf-ac-root", chatID, nil, Active())
	insertTestThreadForWorkflow(t, repo, "th-ac-spawn", chatID, "wf-ac-root", ThreadStatusRunning)
	insertTestQuestion(t, repo, "q-blocking", chatID, "wf-ac-root", "th-ac-spawn")
	require.NoError(t, repo.CreateQuestion(ctx, &Question{
		ID: "q-async", ChatID: chatID, WorkflowID: "wf-ac-root", ThreadID: "th-ac-spawn",
		StepID: "execute_tools", Status: QuestionStatusPending, Async: true, CreatedAt: time.Now().UTC(),
	}))

	now := time.Now().UTC()
	_, err := repo.UpdateThreadStatus(ctx, "th-ac-spawn", ThreadStatusCompleted, &now)
	require.NoError(t, err)
	requireQuestionStatus(t, repo, "q-blocking", QuestionStatusResolved)
	requireQuestionStatus(t, repo, "q-async", QuestionStatusPending)

	// The terminal cascade over the whole subtree leaves it too.
	require.NoError(t, repo.CascadeTerminalStatusToThreadSubtree(ctx, "wf-ac-root", StopReasonCancelled))
	requireQuestionStatus(t, repo, "q-async", QuestionStatusPending)
	assert.Equal(t, []string{"q-blocking"}, resolvedQuestionUpdates(t, repo, chatID))
}
