// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// Story 02: a workflow with ask enabled reaches the ask_question node, blocks
// on a pending question, is resumed twice through the production
// QuestionService — once WITH feedback (loop re-enters, another LLM turn) and
// once with plain "Continue" (loop exits) — and then completes.
//
// Pins down the signal-based pause/resume machinery: QuestionCreate activity
// persists the pending question, ResolveQuestion signals
// "signal.question.<id>" back into the running Temporal workflow,
// has_feedback routes the loop, and the user's feedback reply is persisted
// into the thread.
func TestStory02_AskQuestionPauseResumeCompletes(t *testing.T) {
	t.Parallel()

	feedback := "Please also mention the weather. (e2e-feedback-" + shortID() + ")"

	script := NewScriptedLLM(
		// Turn 1: no tool calls + ask=true → ask_question pauses the loop.
		Turn{Text: "Here is my first answer."},
		// Turn 2: runs after the user replies with feedback.
		Turn{Text: "Updated answer incorporating your feedback."},
	)

	h := newHarness(t, script)

	created := h.CreateChat("builtin://agent", "Answer my question, then wait for my feedback", map[string]any{
		"mode": "auto",
		"ask":  true,
	})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	// 1. The workflow must block on a pending question (not complete, not fail).
	q1 := h.WaitPendingQuestion(chatID)
	require.Equal(t, workflowID, q1.WorkflowID)
	require.NotNil(t, q1.Metadata, "ask_user metadata must be persisted with the question")
	assert.Contains(t, *q1.Metadata, "ask_user")

	wf, err := h.Stack.Repo.GetWorkflow(h.Ctx, workflowID)
	require.NoError(t, err)
	require.Equal(t, db.WorkflowStatusRunning, wf.Status,
		"ask_question blocks inside a running workflow (signal-based, not a terminal pause)")

	// 2. Resolve WITH feedback → has_feedback=true → the loop re-enters and
	// the scripted turn 2 plays.
	h.ResolveQuestion(q1.ID, []string{"Provide feedback"}, feedback)

	// 3. The loop asks again after the second turn (ask is still true).
	q2 := h.WaitPendingQuestion(chatID)
	require.NotEqual(t, q1.ID, q2.ID, "second ask_question must create a fresh question")

	// 4. Resolve with Continue and no freetext → has_feedback=false → loop
	// exits → workflow completes.
	h.ResolveQuestion(q2.ID, []string{"Continue"}, "")

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.WorkflowStatusCompleted)

	// 5. Persistence: both assistant turns and the user's feedback reply are
	// in the thread, in order.
	msgs := h.Messages(chatID, workflowID)
	var texts []string
	var sawFeedbackReply bool
	for _, m := range msgs {
		texts = append(texts, TextOf(m))
		if m.Role == reliantv1.MessageRole_MESSAGE_ROLE_USER && TextOf(m) == feedback {
			sawFeedbackReply = true
		}
	}
	assert.Contains(t, texts, "Here is my first answer.")
	assert.Contains(t, texts, "Updated answer incorporating your feedback.")
	assert.True(t, sawFeedbackReply, "the feedback reply must be saved as a user message in the thread; got messages: %v", texts)

	// The feedback must actually reach the second LLM call's history.
	streamCalls := h.LLM.StreamCalls()
	require.Len(t, streamCalls, 2)
	var feedbackInHistory bool
	for i := range streamCalls[1].Messages {
		hm := &streamCalls[1].Messages[i]
		if hm.Content().Text == feedback {
			feedbackInHistory = true
		}
	}
	assert.True(t, feedbackInHistory, "user feedback must be in the second LLM call's conversation history")

	// No pending question left; chat idle; script fully consumed.
	pending, err := h.Stack.Repo.GetPendingQuestionByChatID(h.Ctx, chatID)
	if err == nil {
		assert.Nil(t, pending, "no pending question after completion")
	}
	assert.Equal(t, db.ChatStateIdle, h.Chat(chatID).State)
	assert.False(t, h.LLM.Exhausted())
}
