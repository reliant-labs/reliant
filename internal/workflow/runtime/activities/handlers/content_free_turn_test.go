// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A turn that comes back from the provider with no text, no thinking and no
// tool calls used to end the chat, silently.
//
// SaveMessage refuses a blockless assistant row (correctly — see
// blockless_assistant_test.go), the step executor logs that refusal and carries
// on, the agent loop finds no tool calls and exits, and the workflow reports
// success. Nothing reaches the transcript and nothing reaches the screen.
//
// It is now reported as an error, on the surface WorkflowErrorMessage.tsx
// already renders. Not as an assistant message: the model said nothing, and a
// fabricated turn would enter the provider history for every turn after it.
//
// Reproduced from chat 58cc003f "Website Review And Analysis": one tool round
// in, the provider answered with stop_reason "refusal" and zero content blocks,
// and the chat stopped dead at 23:27:31 with no message and no error.

func TestContentFreeTurn_RefusalIsExplained(t *testing.T) {
	text, ok := contentFreeTurnExplanation(false, "", "", 0, message.FinishReasonRefusal)

	require.True(t, ok, "a refusal with no content must be reported — without it the chat dies silently")
	assert.NotEmpty(t, strings.TrimSpace(text))
	assert.Contains(t, strings.ToLower(text), "safety", "the text has to say what happened; the provider sends no explanation of its own")
}

func TestContentFreeTurn_UnknownReasonIsStillExplained(t *testing.T) {
	// The reason is informational. Whatever it is, an empty turn must not be
	// handed back empty — that is the shape SaveMessage cannot persist.
	for _, reason := range []message.FinishReason{
		message.FinishReasonUnknown,
		message.FinishReasonEndTurn,
		message.FinishReasonMaxTokens,
		message.FinishReasonError,
	} {
		text, ok := contentFreeTurnExplanation(false, "", "", 0, reason)
		require.True(t, ok, "reason %q: an empty turn must be reported", reason)
		assert.NotEmpty(t, strings.TrimSpace(text), "reason %q", reason)
	}
}

func TestContentFreeTurn_LeavesRealTurnsAlone(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		_, ok := contentFreeTurnExplanation(false, "here you go", "", 0, message.FinishReasonEndTurn)
		assert.False(t, ok)
	})
	t.Run("tool calls only", func(t *testing.T) {
		_, ok := contentFreeTurnExplanation(false, "", "", 2, message.FinishReasonToolUse)
		assert.False(t, ok)
	})
	t.Run("thinking only", func(t *testing.T) {
		// A thinking-only turn is already persistable: SaveMessage counts
		// thinking as content.
		_, ok := contentFreeTurnExplanation(false, "", "reasoning...", 0, message.FinishReasonEndTurn)
		assert.False(t, ok)
	})
}

func TestContentFreeTurn_InterruptIsNotReported(t *testing.T) {
	// A stream the user cancelled before it produced anything is a turn they
	// chose not to have. persistInterruptedTurn already declines to write a row
	// for it; reporting it would flag the user's own cancel as a failure.
	_, ok := contentFreeTurnExplanation(true, "", "", 0, message.FinishReasonRefusal)
	assert.False(t, ok)
}

func TestHandleComplete_CapturesFinishReason(t *testing.T) {
	// The substitution can only name the cause if the reason survives the
	// stream. Nothing else read FinishReason, so nothing was keeping it alive.
	activity := &CallLLMActivity{}
	state := &streamProcessingState{blockStates: NewBlockStreamState()}

	err := activity.processStreamEvent(context.TODO(), "chat-1", "thread-1", llm.DriverEvent{
		Type: llm.EventComplete,
		Response: &llm.DriverResponse{
			FinishReason: message.FinishReasonRefusal,
			Usage:        llm.TokenUsage{TokenCount: 72176},
		},
	}, state)
	require.NoError(t, err)

	assert.Equal(t, message.FinishReasonRefusal, state.finishReason)
}

// The end-to-end assertion the predicate tests cannot make: the error has to
// actually land in chat_updates, in the shape the frontend reads. A correct
// decision that writes nothing is the same silent stop it was before.
func TestReportContentFreeTurn_WritesTheErrorToTheChat(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)

	a := &CallLLMActivity{repo: repo}
	a.reportContentFreeTurn(
		context.Background(),
		chatID,
		"thread-1",
		RuntimeContext{ChatID: chatID, Thread: "thread-1", WorkflowID: "workflow-1"},
		message.FinishReasonRefusal,
		contentFreeTurnText(message.FinishReasonRefusal),
	)

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(context.Background(), chatID)
	require.NoError(t, err)

	var payload map[string]interface{}
	for _, u := range updates {
		var p map[string]interface{}
		if json.Unmarshal([]byte(u.Data), &p) != nil {
			continue
		}
		if p["update_type"] == "error" {
			payload = p
			break
		}
	}
	require.NotNil(t, payload, "no error update was written — the run stopped with nothing to show the user")

	assert.Equal(t, chatID, payload["chat_id"])
	assert.Equal(t, "thread-1", payload["thread"], "an error with no thread renders in every thread of the chat")
	assert.Equal(t, "provider_empty_response", payload["activity_type"])

	// error_summary is the collapsed headline WorkflowErrorMessage shows; without
	// it the UI falls back to mangling activity_type into "Workflow error in ...".
	summary, _ := payload["error_summary"].(string)
	assert.NotEmpty(t, summary)
	assert.Contains(t, strings.ToLower(summary), "declined")

	detail, _ := payload["error_message"].(string)
	assert.Contains(t, detail, string(message.FinishReasonRefusal), "the detail pane has to name the provider's reason")
}

// pause_turn is the one reason in Anthropic's vocabulary that means the model
// did NOT finish: it stopped part-way expecting the conversation handed back.
// Before it was mapped it fell through to Unknown, and a user watching a
// paused turn was told "the model returned an empty response (finish reason:
// unknown)" — wrong about both the cause and the state of the run.
//
// Nothing resumes a paused turn yet, so the requirement here is honesty, not
// recovery: say it paused, and do not claim it ended or that the cause is
// unknown.
func TestContentFreeTurn_PauseTurnIsNotDescribedAsEmptyOrUnknown(t *testing.T) {
	text, ok := contentFreeTurnExplanation(false, "", "", 0, message.FinishReasonPauseTurn)
	require.True(t, ok, "a paused turn with no content still reaches the user with nothing to show")

	lower := strings.ToLower(text)
	assert.Contains(t, lower, "paused", "the text has to name what actually happened")
	assert.NotContains(t, lower, "unknown", "the cause is known; it must not read as the catch-all")

	summary := strings.ToLower(contentFreeTurnSummary(message.FinishReasonPauseTurn))
	assert.Contains(t, summary, "paused")
	assert.NotEqual(t, strings.ToLower(contentFreeTurnSummary(message.FinishReasonUnknown)), summary,
		"a paused turn and an unexplained empty turn must not read identically")
}
