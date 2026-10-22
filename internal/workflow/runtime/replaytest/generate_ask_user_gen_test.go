// Copyright (c) 2025 Reliant Labs
//
//go:build replayfixtures

package replaytest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestGenerateFixture_AskUserYieldsToMessage pins an ask_user wait ended by a
// message to its thread instead of an answer: the agent asks, the user
// messages the thread through the production SendAgentMessage handler (the
// composer's queue), its doorbell ends the wait (the per-observation
// `ask-user-yields-to-thread-input/<question>/1` version marker), the question
// is resolved unanswered (QuestionResolve), and the next CallLLM reads the
// message and completes.
func TestGenerateFixture_AskUserYieldsToMessage(t *testing.T) {
	askCall := ToolCall("call-ask-1", "ask_user",
		`{"blocking":true,"questions":[{"question":"Which style should the page use?","options":[{"label":"Minimal"},{"label":"Bold"}]}]}`)
	script := NewScriptedLLM(
		Turn{Text: "One question before I start.", ToolCalls: []message.ToolCall{askCall}},
		Turn{Text: "Carrying on with sensible defaults, as you asked."},
	)
	h := newHarness(t, script)

	created := h.StartChat("builtin://agent", "Build me a landing page; ask if unsure", map[string]any{
		"mode": "auto",
		// ask_user alone keeps the recorded tool list small.
		"tools": []any{"ask_user"},
	})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	q := h.WaitPendingQuestion(chatID)

	resp, err := h.SendAgentMessage(chatID, workflowID, "continue")
	require.NoError(t, err)
	require.True(t, resp.Success, "SendAgentMessage refused: %s", resp.Message)

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())
	assert.False(t, h.LLM.Exhausted())

	resolved, err := h.Stack.Repo.GetQuestionByID(h.Ctx, q.ID)
	require.NoError(t, err)
	require.Equal(t, db.QuestionStatusResolved, resolved.Status,
		"the question the user did not answer must stop showing as pending")

	h.ExportHistory(workflowID, "ask_user_yields_to_message")
}

// TestGenerateFixture_AskUserAsyncByDefault pins an ask_user call with no flag
// — the default — taking the async path: its `ask-user-async/<tool call>`
// version marker (askUserAsyncByDefault), QuestionCreate for an async
// question, no timer and no signal wait, and the next CallLLM carrying on to
// completion while the question stays pending.
func TestGenerateFixture_AskUserAsyncByDefault(t *testing.T) {
	askCall := ToolCall("call-ask-async-1", "ask_user",
		`{"questions":[{"question":"Which style should the page use? Going with Minimal.","options":[{"label":"Minimal (Recommended)"},{"label":"Bold"}]}]}`)
	script := NewScriptedLLM(
		Turn{Text: "Asking about the style; starting with Minimal meanwhile.", ToolCalls: []message.ToolCall{askCall}},
		Turn{Text: "Built the page in the Minimal style; tell me if you want Bold."},
	)
	h := newHarness(t, script)

	created := h.StartChat("builtin://agent", "Build me a landing page", map[string]any{
		"mode":  "auto",
		"tools": []any{"ask_user"},
	})
	workflowID := created.WorkflowId

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())
	assert.False(t, h.LLM.Exhausted())
	assert.Equal(t, h.LLM.Scripted(), h.LLM.Consumed(), "the loop carried on past the question without waiting")

	asked, err := h.Stack.Repo.ListAsyncQuestionsByChat(h.Ctx, created.Chat.Id)
	require.NoError(t, err)
	require.Len(t, asked, 1, "the question is recorded as async")
	assert.Equal(t, db.QuestionStatusPending, asked[0].Status, "nothing in the run waits on it or closes it")

	h.ExportHistory(workflowID, "ask_user_async_by_default")
}
