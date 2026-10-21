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
		`{"questions":[{"question":"Which style should the page use?","options":[{"label":"Minimal"},{"label":"Bold"}]}]}`)
	script := NewScriptedLLM(
		Turn{Text: "One question before I start.", ToolCalls: []message.ToolCall{askCall}},
		Turn{Text: "Carrying on with sensible defaults, as you asked."},
	)
	h := newHarness(t, script)

	created := h.StartChat("builtin://agent", "Build me a landing page; ask if unsure", map[string]any{
		"mode": "auto",
		// ask_user is loadable, not preloaded: hand it over so the first
		// turn can call it.
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
