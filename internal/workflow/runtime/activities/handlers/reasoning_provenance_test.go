// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assistantFrom(account string) message.Message {
	return message.Message{
		ID: "a-" + account, Role: message.Assistant, ProviderAccountID: account,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "hmm", Signature: "sig"},
			message.RedactedReasoningContent{Data: "opaque"},
			message.TextContent{Text: "answer"},
			message.ToolCall{ID: "call_1", Name: "bash", Input: "{}", Finished: true},
		},
	}
}

func TestStripForeignReasoningCodex(t *testing.T) {
	t.Parallel()

	user := message.Message{ID: "u", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}
	history := []message.Message{user, assistantFrom("acct-a"), assistantFrom("acct-b"), assistantFrom("")}

	got := stripForeignReasoning(history, "codex", "acct-b")

	require.Len(t, got, 4)
	assert.Equal(t, history[0], got[0])
	// other account: reasoning gone, text and tool call kept
	assert.Equal(t, []message.ContentPart{
		message.TextContent{Text: "answer"},
		message.ToolCall{ID: "call_1", Name: "bash", Input: "{}", Finished: true},
	}, got[1].Parts)
	// same account and unknown provenance: untouched
	assert.Equal(t, history[2], got[2])
	assert.Equal(t, history[3], got[3])
}

func TestStripForeignReasoningOpenAIAlsoOrgBound(t *testing.T) {
	t.Parallel()

	got := stripForeignReasoning([]message.Message{assistantFrom("a")}, "openai", "b")
	assert.Len(t, got[0].Parts, 2)
}

func TestStripForeignReasoningLeavesAnthropicAlone(t *testing.T) {
	t.Parallel()

	history := []message.Message{assistantFrom("acct-a")}
	for _, driver := range []string{"claude", "anthropic"} {
		got := stripForeignReasoning(history, models.DriverID(driver), "acct-b")
		assert.Equal(t, history, got, driver)
	}
}

func TestStripForeignReasoningNeverMutatesHistory(t *testing.T) {
	t.Parallel()

	history := []message.Message{assistantFrom("acct-a")}
	before := len(history[0].Parts)

	got := stripForeignReasoning(history, "codex", "acct-b")

	assert.Len(t, history[0].Parts, before, "stored history is not modified")
	assert.IsType(t, message.ReasoningContent{}, history[0].Parts[0])
	assert.NotSame(t, &history[0], &got[0])
}

func TestStripForeignReasoningNoTargetAccountIsNoop(t *testing.T) {
	t.Parallel()

	history := []message.Message{assistantFrom("acct-a")}
	assert.Equal(t, history, stripForeignReasoning(history, "codex", ""))
	assert.Nil(t, stripForeignReasoning(nil, "codex", "b"))
}
