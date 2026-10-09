// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingSummaryDriver counts summarization requests.
type countingSummaryDriver struct {
	mockLLMDriverForIdempotency
	calls atomic.Int32
}

func (d *countingSummaryDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tls []tools.Tool) <-chan llm.DriverEvent {
	d.calls.Add(1)
	return d.mockLLMDriverForIdempotency.StreamResponse(ctx, prompts, messages, tls)
}

func (d *countingSummaryDriver) resolver() drivers.DriverResolver {
	return func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return d, nil
	}
}

// capturedSkips records what compactionSkipNotices would send to Sentry.
type capturedSkips struct {
	errs  []error
	tags  []map[string]string
	extra []map[string]interface{}
}

func (c *capturedSkips) notices() *compactionSkipNotices {
	return &compactionSkipNotices{capture: func(err error, tags map[string]string, extra map[string]interface{}) string {
		c.errs = append(c.errs, err)
		c.tags = append(c.tags, tags)
		c.extra = append(c.extra, extra)
		return "event"
	}}
}

// saveTurn persists one agent-loop turn: an assistant tool call whose provider
// count is tokenCount, and the tool result it produced.
func saveTurn(t *testing.T, svc *threads.Service, chatID, thread, callID string, tokenCount int, resultChars int) {
	t.Helper()
	ctx := context.Background()
	_, err := svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID: chatID, Thread: thread, Role: roleAssistant,
		ToolCalls:  []message.ToolCall{{ID: callID, Name: "shell", Input: `{"command":"ls"}`}},
		TokenCount: tokenCount,
		Model:      "gpt-5.6-terra",
	})
	require.NoError(t, err)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID: chatID, Thread: thread, Role: roleTool,
		ToolResults: []message.ToolResult{{ToolCallID: callID, Name: "shell", Content: strings.Repeat("x", resultChars)}},
	})
	require.NoError(t, err)
}

// Prod incident 2026-10-09, thread ee7dcaa4 (chat 416f3fe9): a spawned
// sub-agent's 209-char brief reported 246,838 tokens on its first turn, above
// its 231,200 threshold. Every compaction produced a window that opened at
// ~245.9k, so the loop compacted after every tool call — 183 times in 44
// minutes. The guard must decline that compaction: no summarization call, no
// new context window, one report however many turns ask.
func TestCompact_SkipsCompactionThatCannotShrinkTheContext(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	chatID, thread, svc := setupChatWithWorkflowAndThread(t, h.Repo())
	_, err := h.Repo().SaveMessageToThread(ctx, chatID, thread, roleUser,
		"Work in reliant-compose-stack branch fix/docker-compose-stack. Apply the previously described compose fixes comprehensively.",
		&thread, nil, nil)
	require.NoError(t, err)
	saveTurn(t, svc, chatID, thread, "call_1", 246_838, 8_707)

	driver := &countingSummaryDriver{}
	captured := &capturedSkips{}
	compact := NewCompactActivity(h.Repo(), driver.resolver())
	compact.skips = captured.notices()

	input := CompactInput{ChatID: chatID, Thread: thread}
	for turn := 0; turn < 3; turn++ {
		var out CompactOutput
		require.NoError(t, h.ExecuteActivity(compact.Execute, input, &out))
		assert.Contains(t, out.GetSkippedReason(), "fixed base", "turn %d", turn)
		assert.Nil(t, out.GetMessage(), "turn %d: a skipped compaction has no summary", turn)
	}

	assert.Zero(t, driver.calls.Load(), "a compaction that cannot shrink the context must not call the summarizer")
	seq, err := h.Repo().GetMaxSequenceForThread(ctx, thread)
	require.NoError(t, err)
	assert.Zero(t, seq, "no new context window may be opened")

	require.Len(t, captured.errs, 1, "the stuck window is reported once, not once per turn")
	assert.ErrorIs(t, captured.errs[0], errCompactionCannotReclaim)
	assert.Equal(t, 246_838, captured.extra[0]["window_floor_tokens"])
	assert.Equal(t, "gpt-5.6-terra", captured.tags[0]["model"])
}

// A compaction the user asked for (the internal "compact" workflow behind
// CompactChat sets force) is not a loop: it runs even when it frees little.
func TestCompact_ForceCompactsEvenWhenItCannotShrinkTheContext(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	chatID, thread, svc := setupChatWithWorkflowAndThread(t, h.Repo())
	_, err := h.Repo().SaveMessageToThread(ctx, chatID, thread, roleUser, "Summarize where we are.", &thread, nil, nil)
	require.NoError(t, err)
	saveTurn(t, svc, chatID, thread, "call_1", 246_838, 8_707)

	driver := &countingSummaryDriver{}
	captured := &capturedSkips{}
	compact := NewCompactActivity(h.Repo(), driver.resolver())
	compact.skips = captured.notices()

	input := CompactInput{ChatID: chatID, Thread: thread}.V3()
	input.Node.GetCompact().Force = &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}}
	var out CompactOutput
	require.NoError(t, h.ExecuteActivity(compact.Execute, input, &out))

	assert.Empty(t, out.GetSkippedReason())
	assert.EqualValues(t, 1, driver.calls.Load())
	assert.Empty(t, captured.errs)
}

// The guard must not stand in the way of a thread whose history is what fills
// the window — the case compaction exists for.
func TestCompact_CompactsWhenHistoryIsReclaimable(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	chatID, thread, svc := setupChatWithWorkflowAndThread(t, h.Repo())
	_, err := h.Repo().SaveMessageToThread(ctx, chatID, thread, roleUser, "Refactor the billing module.", &thread, nil, nil)
	require.NoError(t, err)
	saveTurn(t, svc, chatID, thread, "call_1", 40_000, 4_000)
	saveTurn(t, svc, chatID, thread, "call_2", 231_500, 2_000)

	driver := &countingSummaryDriver{}
	captured := &capturedSkips{}
	compact := NewCompactActivity(h.Repo(), driver.resolver())
	compact.skips = captured.notices()

	var out CompactOutput
	require.NoError(t, h.ExecuteActivity(compact.Execute, CompactInput{ChatID: chatID, Thread: thread}, &out))

	assert.Empty(t, out.GetSkippedReason())
	require.NotNil(t, out.GetMessage())
	assert.Contains(t, out.GetMessage().GetText(), "Mock response")
	assert.EqualValues(t, 1, driver.calls.Load())
	seq, err := h.Repo().GetMaxSequenceForThread(ctx, thread)
	require.NoError(t, err)
	assert.Equal(t, 1, seq, "compaction opens the next context window")
	assert.Empty(t, captured.errs)
}

func TestCompactionSkipNotices_ReportOncePerWindow(t *testing.T) {
	captured := &capturedSkips{}
	notices := captured.notices()
	reclaim := message.CompactionReclaim{Current: 250_000, Floor: 245_918, Reclaimable: 4_200}

	assert.True(t, notices.report(compactionSkip{Thread: "a", ContextSequence: 1, Reclaim: reclaim}))
	assert.False(t, notices.report(compactionSkip{Thread: "a", ContextSequence: 1, Reclaim: reclaim}), "same window")
	assert.True(t, notices.report(compactionSkip{Thread: "a", ContextSequence: 2, Reclaim: reclaim}), "a later window that is just as stuck")
	assert.True(t, notices.report(compactionSkip{Thread: "b", ContextSequence: 1, Reclaim: reclaim}), "another thread")
	assert.Len(t, captured.errs, 3)
}

func TestNewCompactionSkip_NamesTheWindowAndModel(t *testing.T) {
	skip := newCompactionSkip("chat", "thread", []message.Message{
		{Role: message.System, ContextSequence: 4},
		{Role: message.Assistant, ContextSequence: 4, TokenCount: 245_918, Model: "gpt-5.6-terra"},
		{Role: message.Tool, ContextSequence: 4},
	}, message.CompactionReclaim{})
	assert.Equal(t, int64(4), skip.ContextSequence)
	assert.Equal(t, "gpt-5.6-terra", skip.Model)
}
