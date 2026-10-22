// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// list_pending_questions and withdraw_question are scoped to the caller's
// thread subtree by the store (internal/db's question_subtree_test.go pins the
// scoping against a real database); these pin what the tools do with it.

type fakeQuestionStore struct {
	rows      []db.SubtreeQuestion
	listedFor [2]string

	outcome   db.WithdrawOutcome
	withdrawn []string
}

func (f *fakeQuestionStore) ListPendingQuestionsInThreadSubtree(_ context.Context, chatID, threadID string) ([]db.SubtreeQuestion, error) {
	f.listedFor = [2]string{chatID, threadID}
	return f.rows, nil
}

func (f *fakeQuestionStore) WithdrawAsyncQuestion(_ context.Context, chatID, threadID, questionID, reason string) (db.WithdrawOutcome, error) {
	f.withdrawn = append(f.withdrawn, chatID+"|"+threadID+"|"+questionID+"|"+reason)
	return f.outcome, nil
}

func questionToolContext() *rctx.ToolContext {
	return &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1", Thread: "thread-a"}
}

func runQuestionTool(t *testing.T, tool Tool, name string, params interface{}) ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(questionToolContext(), ToolCall{ID: "call-1", Name: name, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func TestListPendingQuestions_ListsTheCallersSubtree(t *testing.T) {
	t.Parallel()
	title := "schema researcher"
	asyncMeta := `{"type":"ask_user","async":true,"questions":[{"question":"Which database?","options":[{"label":"Postgres (Recommended)","description":"d"},{"label":"SQLite","description":"d"}]}]}`
	blockingMeta := `{"type":"ask_user","questions":[{"question":"Drop the old table?","options":[]}]}`
	store := &fakeQuestionStore{rows: []db.SubtreeQuestion{
		{ID: "q-own", ThreadID: "thread-a", Async: true, Metadata: &asyncMeta, CreatedAt: time.Now().Add(-5 * time.Minute)},
		{ID: "q-child", ThreadID: "thread-a1", ThreadTitle: &title, Metadata: &blockingMeta, CreatedAt: time.Now().Add(-time.Minute)},
	}}

	resp := runQuestionTool(t, NewListPendingQuestionsTool(store), ListPendingQuestionsToolName, ListPendingQuestionsParams{})

	assert.False(t, resp.IsError)
	assert.Equal(t, [2]string{"chat-1", "thread-a"}, store.listedFor, "scoped to the caller's own thread")
	assert.Contains(t, resp.Content, "question_id: q-own · async")
	assert.Contains(t, resp.Content, "asked by you")
	assert.Contains(t, resp.Content, "Q: Which database?")
	assert.Contains(t, resp.Content, "options: Postgres (Recommended) | SQLite")
	assert.Contains(t, resp.Content, "question_id: q-child · BLOCKING")
	assert.Contains(t, resp.Content, `asked by sub-agent thread-a1 ("schema researcher")`)

	var md ListPendingQuestionsResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &md))
	require.Len(t, md.Questions, 2)
	assert.Equal(t, "you", md.Questions[0].AskedBy)
	assert.True(t, md.Questions[0].Async)
	assert.Equal(t, []string{"Postgres (Recommended)", "SQLite"}, md.Questions[0].Questions[0].Options)
	assert.GreaterOrEqual(t, md.Questions[0].WaitingMs, int64(4*time.Minute/time.Millisecond))
	assert.Equal(t, "sub-agent", md.Questions[1].AskedBy)
	assert.Equal(t, "schema researcher", md.Questions[1].Title)
	assert.False(t, md.Questions[1].Async)
}

func TestListPendingQuestions_Empty(t *testing.T) {
	t.Parallel()
	resp := runQuestionTool(t, NewListPendingQuestionsTool(&fakeQuestionStore{}), ListPendingQuestionsToolName, ListPendingQuestionsParams{})
	assert.False(t, resp.IsError)
	assert.Equal(t, "No unanswered questions from you or your sub-agents.", resp.Content)
}

func TestWithdrawQuestion(t *testing.T) {
	t.Parallel()

	t.Run("withdraws within the caller's subtree, with the reason", func(t *testing.T) {
		t.Parallel()
		store := &fakeQuestionStore{outcome: db.QuestionWithdrawn}
		resp := runQuestionTool(t, NewWithdrawQuestionTool(store), WithdrawQuestionToolName,
			WithdrawQuestionParams{QuestionID: "q-1", Reason: "Went with Postgres"})
		assert.False(t, resp.IsError)
		assert.Equal(t, []string{"chat-1|thread-a|q-1|Went with Postgres"}, store.withdrawn)
		assert.Contains(t, resp.Content, "Withdrew question q-1")
	})

	t.Run("refuses a blocking question", func(t *testing.T) {
		t.Parallel()
		store := &fakeQuestionStore{outcome: db.QuestionWithdrawBlocking}
		resp := runQuestionTool(t, NewWithdrawQuestionTool(store), WithdrawQuestionToolName,
			WithdrawQuestionParams{QuestionID: "q-1", Reason: "moot"})
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "is blocking")
	})

	t.Run("says nothing about a question outside the subtree", func(t *testing.T) {
		t.Parallel()
		store := &fakeQuestionStore{outcome: db.QuestionWithdrawNotFound}
		resp := runQuestionTool(t, NewWithdrawQuestionTool(store), WithdrawQuestionToolName,
			WithdrawQuestionParams{QuestionID: "q-sibling", Reason: "moot"})
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "No unanswered question q-sibling from you or your sub-agents")
	})

	t.Run("requires a reason, which the user sees", func(t *testing.T) {
		t.Parallel()
		store := &fakeQuestionStore{}
		resp := runQuestionTool(t, NewWithdrawQuestionTool(store), WithdrawQuestionToolName,
			WithdrawQuestionParams{QuestionID: "q-1", Reason: "  "})
		assert.True(t, resp.IsError)
		assert.Empty(t, store.withdrawn)
	})
}

// Handed to an agent that can ask, or that has sub-agents that might have;
// nobody else has a question to list.
func TestResolveCapabilities_QuestionToolsFollowAsking(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   CapabilityInputs
		want bool
	}{
		{name: "an agent that cannot ask and has no sub-agents", want: false},
		{name: "an agent that can ask", in: CapabilityInputs{Access: ResolveToolAccess([]string{ToolAskUser}, nil, nil)}, want: true},
		{name: "an orchestrator whose sub-agents may ask", in: CapabilityInputs{OwnChildren: true}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Permission = PermissionMutating
			caps := ResolveCapabilities(tc.in)
			assert.Equal(t, tc.want, caps.Offers(ToolListPendingQuestions))
			assert.Equal(t, tc.want, caps.Offers(ToolWithdrawQuestion))
		})
	}
}
