// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// list_pending_questions and withdraw_question see an agent's own questions
// and those of every sub-agent below it, never a sibling's; only an async
// question can be withdrawn, and withdrawing takes it out of the inbox.

// questionTreeChat is a chat whose root spawned A and B, and A spawned A1:
//
//	root ─┬─ A ── A1
//	      └─ B
type questionTreeChat struct {
	*liveGateChat
}

func newQuestionTreeChat(t *testing.T) *questionTreeChat {
	t.Helper()
	c := &questionTreeChat{newLiveGateChat(t, "chat-question-tree")}
	c.spawn("sa-a", Active())
	c.spawn("sa-b", Active())
	parent := "sa-a"
	child := "sa-a1"
	_, err := c.repo.CreateThread(context.Background(), &Thread{
		ID: child, ChatID: c.chatID, ParentThreadID: &parent, Origin: ThreadOriginSpawn,
		Status: ThreadStatusRunning, WorkflowID: &child, CreatedAt: time.Now(),
	})
	require.NoError(t, err)
	c.run(child, child, Active(), time.Now())

	c.askAsync("q-root", c.chatID, c.chatID)
	c.askAsync("q-a", "sa-a", "sa-a")
	c.ask("q-a1", "sa-a1", "sa-a1") // blocking
	c.askAsync("q-b", "sa-b", "sa-b")
	return c
}

func (c *questionTreeChat) listFrom(thread string) []string {
	c.t.Helper()
	rows, err := c.repo.ListPendingQuestionsInThreadSubtree(context.Background(), c.chatID, thread)
	require.NoError(c.t, err)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestPendingQuestionsInThreadSubtree_Scoping(t *testing.T) {
	c := newQuestionTreeChat(t)

	assert.ElementsMatch(t, []string{"q-root", "q-a", "q-a1", "q-b"}, c.listFrom(c.chatID), "the root sees the whole chat")
	assert.ElementsMatch(t, []string{"q-a", "q-a1"}, c.listFrom("sa-a"), "A sees its own and its sub-agent's, not B's")
	assert.Equal(t, []string{"q-b"}, c.listFrom("sa-b"), "B sees only its own")
	assert.Equal(t, []string{"q-a1"}, c.listFrom("sa-a1"))

	rows, err := c.repo.ListPendingQuestionsInThreadSubtree(context.Background(), c.chatID, "sa-a")
	require.NoError(t, err)
	byID := map[string]SubtreeQuestion{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	assert.True(t, byID["q-a"].Async)
	assert.False(t, byID["q-a1"].Async, "blocking questions are listed too")
	assert.Equal(t, "sa-a1", byID["q-a1"].ThreadID)

	// Answered questions are not pending.
	require.NoError(t, c.repo.ResolveQuestion(context.Background(), "q-b", nil))
	assert.Empty(t, c.listFrom("sa-b"))
}

func TestWithdrawAsyncQuestion(t *testing.T) {
	c := newQuestionTreeChat(t)
	ctx := context.Background()

	// A sibling's question: not found, and untouched.
	outcome, err := c.repo.WithdrawAsyncQuestion(ctx, c.chatID, "sa-a", "q-b", "moot")
	require.NoError(t, err)
	assert.Equal(t, QuestionWithdrawNotFound, outcome)
	requireQuestionStatus(t, c.repo, "q-b", QuestionStatusPending)

	// A blocking one in the subtree: refused, untouched.
	outcome, err = c.repo.WithdrawAsyncQuestion(ctx, c.chatID, "sa-a", "q-a1", "moot")
	require.NoError(t, err)
	assert.Equal(t, QuestionWithdrawBlocking, outcome)
	requireQuestionStatus(t, c.repo, "q-a1", QuestionStatusPending)

	require.Contains(t, c.inboxQuestionKeys(), "q-a")

	// Its own sub-agent's async question, from the root: withdrawn.
	outcome, err = c.repo.WithdrawAsyncQuestion(ctx, c.chatID, c.chatID, "q-a", "Went with Postgres")
	require.NoError(t, err)
	assert.Equal(t, QuestionWithdrawn, outcome)
	q := requireQuestionStatus(t, c.repo, "q-a", QuestionStatusResolved)
	require.NotNil(t, q.WithdrawnReason)
	assert.Equal(t, "Went with Postgres", *q.WithdrawnReason)
	assert.Nil(t, q.ResponseData, "withdrawn, not answered")

	assert.NotContains(t, c.inboxQuestionKeys(), "q-a", "a withdrawn question leaves the inbox")
	assert.NotContains(t, c.listFrom(c.chatID), "q-a")

	// The chat's feed tells the timeline card why.
	updates, err := c.repo.GetLatestNonMessageUpdatesPerEntity(ctx, c.chatID)
	require.NoError(t, err)
	var withdrawn map[string]interface{}
	for _, u := range updates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION {
			continue
		}
		var payload map[string]interface{}
		require.NoError(t, json.Unmarshal(u.Data, &payload))
		if payload["question_id"] == "q-a" {
			withdrawn = payload
		}
	}
	require.NotNil(t, withdrawn)
	assert.Equal(t, "resolved", withdrawn["status"])
	assert.Equal(t, true, withdrawn["async"])
	assert.Equal(t, "Went with Postgres", withdrawn["withdrawn_reason"])

	// Twice: nothing pending to withdraw.
	outcome, err = c.repo.WithdrawAsyncQuestion(ctx, c.chatID, c.chatID, "q-a", "again")
	require.NoError(t, err)
	assert.Equal(t, QuestionWithdrawNotFound, outcome)
}
