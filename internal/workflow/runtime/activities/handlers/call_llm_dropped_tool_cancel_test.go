// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/rowreads"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/streaming"
)

// A tool call the driver drops — a function call of a Responses response that
// did not complete — must have its card closed, not left "requested".
//
// The driver opened the card with EventToolUseStart under the output item id
// (fc_*), then withheld the call from ToolCalls because the response was
// incomplete. Nothing executes it, so nothing would ever report an outcome:
// the live card stayed "preparing"/"requested" until a reload. The finished
// stream must cancel it exactly as an interrupt does — durable row plus the
// tool_cancelled delta, keyed by the id and block the card was opened under —
// and must leave the call it kept, and calls it never opened, alone.
func TestCompletedStream_CancelsTheToolCallsTheDriverDropped(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	repo := f.h.Repo()
	ctx := context.Background()

	hub := &captureHub{}
	activityInstance := &CallLLMActivity{repo: rowreads.WithRepo(repo), hub: hub}
	state := &streamProcessingState{blockStates: NewBlockStreamState()}

	const keptCard, droppedCard, neverOpened = "fc_kept", "fc_dropped", "fc_never_opened"

	for _, ev := range []llm.DriverEvent{
		{Type: llm.EventToolUseStart, ToolCall: &message.ToolCall{ID: keptCard}},
		{Type: llm.EventToolUseStop, ToolCall: &message.ToolCall{ID: "call_kept", Name: "read"}},
		{Type: llm.EventToolUseStart, ToolCall: &message.ToolCall{ID: droppedCard}},
		{Type: llm.EventComplete, Response: &llm.DriverResponse{
			FinishReason: message.FinishReasonPauseTurn,
			ToolCalls:    []message.ToolCall{{ID: "call_kept", Name: "read", Input: `{"path":"a"}`}},
			DroppedToolCalls: []message.ToolCall{
				{ID: droppedCard, Name: "bash"},
				{ID: neverOpened, Name: "bash"},
			},
		}},
	} {
		require.NoError(t, activityInstance.processStreamEvent(ctx, f.chatID, f.chatID, ev, state))
	}

	// The durable half: the dropped call is over, under its card's id.
	got, err := repo.GetToolCall(ctx, droppedCard)
	require.NoError(t, err, "a dropped call must leave a durable cancelled row")
	require.NotNil(t, got)
	assert.Equal(t, core.ToolCallStatusCancelled, got.Status)
	assert.Equal(t, "bash", got.ToolName, "the name the driver learned after the start is recorded")

	// The ephemeral half: one tool_cancelled delta, at the dropped card's block.
	var cancelled []streaming.StreamingDelta
	droppedBlock := -1
	for _, d := range hub.captured() {
		switch {
		case d.DeltaType == "tool_use_start" && d.ToolCall != nil && d.ToolCall.ID == droppedCard:
			droppedBlock = d.BlockIndex
		case d.DeltaType == streaming.DeltaTypeToolCancelled:
			cancelled = append(cancelled, d)
		}
	}
	require.Len(t, cancelled, 1, "exactly the dropped card is closed")
	require.NotNil(t, cancelled[0].ToolCall)
	assert.Equal(t, droppedCard, cancelled[0].ToolCall.ID)
	assert.Equal(t, droppedBlock, cancelled[0].BlockIndex, "the delta must target the block the card was opened at")

	// The kept call runs; it must not be cancelled. A call never opened has
	// no card, so it gets no row.
	for _, id := range []string{keptCard, "call_kept", neverOpened} {
		row, err := repo.GetToolCall(ctx, id)
		if err == nil && row != nil {
			assert.NotEqual(t, core.ToolCallStatusCancelled, row.Status, "%s must not be cancelled", id)
		}
	}

	// A later interrupt pass must not cancel the dropped call a second time.
	activityInstance.cancelInFlightStreamingToolCalls(ctx, f.chatID, f.chatID, state)
	again := 0
	for _, d := range hub.captured() {
		if d.DeltaType == streaming.DeltaTypeToolCancelled && d.ToolCall != nil && d.ToolCall.ID == droppedCard {
			again++
		}
	}
	assert.Equal(t, 1, again, "a settled call is cancelled once")
}
