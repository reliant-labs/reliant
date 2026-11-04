// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prodOverflowError is the refusal that ended two sub-agents in prod on
// 2026-10-26: a ~740k-token codex thread summarized on a 1M Claude window.
const prodOverflowError = `streaming failed: POST "https://api.anthropic.com/v1/messages": 400 Bad Request (Request-ID: req_011CgRKXPxkD2vDeNMhFvqXb) (Workspace-ID: wrkspc_x) {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1114231 tokens > 1000000 maximum"},"request_id":"req_011CgRKXPxkD2vDeNMhFvqXb"}`

func TestParseContextOverflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		want       contextOverflow
		wantIsOver bool
	}{
		{"anthropic, with counts", errors.New(prodOverflowError), contextOverflow{Actual: 1114231, Limit: 1000000}, true},
		{"openai chat completions, with counts", errors.New("This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens."), contextOverflow{Actual: 130000, Limit: 128000}, true},
		{"gemini, with counts", errors.New("The input token count (1200000) exceeds the maximum number of tokens allowed (1048576)."), contextOverflow{Actual: 1200000, Limit: 1048576}, true},
		{"codex, no counts", errors.New(`{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`), contextOverflow{}, true},
		{"an unrelated 400", errors.New("400 Bad Request: invalid model"), contextOverflow{}, false},
		{"nil", nil, contextOverflow{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, isOver := parseContextOverflow(tt.err)
			assert.Equal(t, tt.wantIsOver, isOver)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestContextOverflowKeepFraction(t *testing.T) {
	t.Parallel()

	assert.InDelta(t, 1000000.0/1114231.0*overflowSafetyMargin, contextOverflow{Actual: 1114231, Limit: 1000000}.keepFraction(), 1e-9)
	assert.InDelta(t, overflowFallbackKeep, contextOverflow{}.keepFraction(), 1e-9)
}

func historyChars(history []message.Message) int {
	total := 0
	for _, m := range history {
		total += len(m.Content().Text)
	}
	return total
}

func TestSummaryPrefixEnd_PreservesWholeMessagesAtAllBudgets(t *testing.T) {
	t.Parallel()
	history := []message.Message{textMsg(message.User, "é界 original task"), textMsg(message.Assistant, "results")}
	text, boundaries := renderCompactionTranscript(history)
	for budget := -1; budget <= len(text)+1; budget++ {
		end := summaryPrefixEnd(boundaries, budget)
		assert.LessOrEqual(t, end, max(budget, 0))
		assert.Equal(t, text, text[:end]+text[end:])
		assert.True(t, end == 0 || end == boundaries[0] || end == boundaries[1])
		assert.Equal(t, text[:end], strings.ToValidUTF8(text[:end], ""))
		assert.Equal(t, text[end:], strings.ToValidUTF8(text[end:], ""))
	}
}

func TestNextSummaryPrefixBudget_ReservesOverheadAndTerminates(t *testing.T) {
	t.Parallel()
	assert.Zero(t, nextSummaryPrefixBudget(0, 700, contextOverflow{}))
	assert.Zero(t, nextSummaryPrefixBudget(minimumSummaryPrefixBytes, 700, contextOverflow{}))
	assert.Equal(t, 4300, nextSummaryPrefixBudget(9300, 700, contextOverflow{}))
	assert.Equal(t, minimumSummaryPrefixBytes, nextSummaryPrefixBudget(1025, 700, contextOverflow{Actual: 1_000_000, Limit: 1}))
}

// overflowingSummarizer refuses the first `refusals` requests as too long and
// answers the next, recording every history it was sent.
type overflowingSummarizer struct {
	mockLLMDriverForIdempotency
	refusals int
	err      error
	got      [][]message.Message
}

func (d *overflowingSummarizer) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tls []tools.Tool) <-chan llm.DriverEvent {
	d.got = append(d.got, append([]message.Message(nil), messages...))
	if len(d.got) <= d.refusals {
		ch := make(chan llm.DriverEvent, 1)
		ch <- llm.DriverEvent{Type: llm.EventError, Error: d.err}
		close(ch)
		return ch
	}
	return d.mockLLMDriverForIdempotency.StreamResponse(ctx, prompts, messages, tls)
}

func (d *overflowingSummarizer) resolver() func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
	return func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return d, nil
	}
}

func longConversation() []message.Message {
	history := []message.Message{textMsg(message.User, "the original task")}
	for i := range 40 {
		history = append(history,
			textMsg(message.Assistant, fmt.Sprintf("step %d %s", i, strings.Repeat("y", 2000))),
			textMsg(message.User, fmt.Sprintf("ok %d", i)))
	}
	return history
}

func TestGenerateCompactionSummary_RecoversFromContextOverflow(t *testing.T) {
	t.Parallel()

	driver := &overflowingSummarizer{refusals: 1, err: errors.New(prodOverflowError)}
	compact := NewCompactActivity(nil, driver.resolver())

	summary, err := compact.generateCompactionSummary(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, "", "", types.FailoverPolicy{}, longConversation(), nil)
	require.NoError(t, err, "a summarizer that refuses the history as too long must not fail the compaction")
	assert.Contains(t, summary, "Mock response")
	assert.Contains(t, summary, unprocessedContinuation)

	require.Len(t, driver.got, 2)
	first, retry := driver.got[0], driver.got[1]
	assert.Less(t, historyChars(retry), historyChars(first)*9/10, "the retry makes bounded progress")
	assert.Contains(t, retry[0].Content().Text, "the original task")
	assert.Contains(t, retry[len(retry)-1].Content().Text, "handoff", "the summarization instruction is still the last turn")
}

func TestGenerateCompactionSummary_GivesUpAfterBoundedRetries(t *testing.T) {
	t.Parallel()

	driver := &overflowingSummarizer{refusals: 100, err: errors.New(prodOverflowError)}
	compact := NewCompactActivity(nil, driver.resolver())

	_, err := compact.generateCompactionSummary(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, "", "", types.FailoverPolicy{}, longConversation(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt is too long")
	assert.ErrorIs(t, err, errCompactionOverflow)
	assert.Less(t, len(driver.got), 20)
	assert.Greater(t, len(driver.got), 3)
}

func TestGenerateCompactionSummary_DoesNotRetryOtherFailures(t *testing.T) {
	t.Parallel()

	driver := &overflowingSummarizer{refusals: 100, err: errors.New("400 Bad Request: invalid model")}
	compact := NewCompactActivity(nil, driver.resolver())

	_, err := compact.generateCompactionSummary(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, "", "", types.FailoverPolicy{}, longConversation(), nil)
	require.Error(t, err)
	assert.Len(t, driver.got, 1)
}
