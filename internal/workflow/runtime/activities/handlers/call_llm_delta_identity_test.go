// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/streaming"
)

// captureHub records every published delta so tests can assert on the full
// stream. Implements streaming.StreamingHub.
type captureHub struct {
	mu     sync.Mutex
	deltas []streaming.StreamingDelta
}

func (h *captureHub) Publish(_ context.Context, _ string, delta streaming.StreamingDelta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deltas = append(h.deltas, delta)
}

func (h *captureHub) PublishEvent(ctx context.Context, event streaming.ChatEvent) {
	h.Publish(ctx, event.ChatID, event.Delta)
}

func (h *captureHub) Subscribe(context.Context, string) streaming.Subscription { return nil }
func (h *captureHub) SubscriberCount(string) int                               { return 0 }
func (h *captureHub) TotalSubscriberCount() int                                { return 0 }
func (h *captureHub) Stats() streaming.HubStats                                { return streaming.HubStats{} }
func (h *captureHub) IsConnected() bool                                        { return true }
func (h *captureHub) Close() error                                             { return nil }

func (h *captureHub) captured() []streaming.StreamingDelta {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]streaming.StreamingDelta, len(h.deltas))
	copy(out, h.deltas)
	return out
}

// TestCallLLM_DeltaIdentity pins the delta identity protocol on the producer:
// when the workflow pre-allocates an assistant message id, every message delta
// carries that id plus a strictly increasing stream_seq, the stream opens with
// a message_start delta, and the activity output echoes id + last seq.
func TestCallLLM_DeltaIdentity(t *testing.T) {
	resolver := mockLLMDriverResolver()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	hub := &captureHub{}
	activityInstance := NewCallLLMActivity(h.Repo(), hub, nil, &staticConfigProvider{}, resolver, nil)

	preallocatedID := uuid.NewString()
	input := callLLMInput(chat.ID, chat.ID, "mock-model")
	input.Runtime.AssistantMessageID = preallocatedID

	var output CallLLMOutput
	err := h.ExecuteActivity(activityInstance.Execute, input, &output)
	require.NoError(t, err)

	deltas := hub.captured()
	require.NotEmpty(t, deltas, "expected streaming deltas to be published")

	// message_start opens the stream so a retry re-stream signals "reset blocks".
	assert.Equal(t, streaming.DeltaTypeMessageStart, deltas[0].DeltaType,
		"first delta must be message_start")

	var lastSeq int64
	for i, d := range deltas {
		assert.Equal(t, preallocatedID, d.MessageID,
			"delta %d (%s) must carry the pre-allocated message id", i, d.DeltaType)
		assert.Greater(t, d.StreamSeq, lastSeq,
			"delta %d (%s) stream_seq must be strictly increasing", i, d.DeltaType)
		lastSeq = d.StreamSeq
	}

	// Output echoes the identity for the finalize marker.
	assert.Equal(t, preallocatedID, output.MessageId)
	assert.Equal(t, lastSeq, output.LastStreamSeq)
}

// TestCallLLM_DeltaIdentity_NoPreallocatedID pins backwards compatibility:
// without a pre-allocated id (legacy histories, replay DefaultVersion) deltas
// stay id-less and no message_start is synthesized.
func TestCallLLM_DeltaIdentity_NoPreallocatedID(t *testing.T) {
	resolver := mockLLMDriverResolver()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	hub := &captureHub{}
	activityInstance := NewCallLLMActivity(h.Repo(), hub, nil, &staticConfigProvider{}, resolver, nil)

	var output CallLLMOutput
	err := h.ExecuteActivity(activityInstance.Execute, callLLMInput(chat.ID, chat.ID, "mock-model"), &output)
	require.NoError(t, err)

	for i, d := range hub.captured() {
		assert.NotEqual(t, streaming.DeltaTypeMessageStart, d.DeltaType,
			"delta %d: no message_start without a pre-allocated id", i)
		assert.Empty(t, d.MessageID, "delta %d must stay id-less", i)
		assert.Zero(t, d.StreamSeq, "delta %d must not carry stream_seq", i)
	}
	assert.Empty(t, output.MessageId)
	assert.Zero(t, output.LastStreamSeq)
}

// TestWriteStreamingDelta_CancelledGate pins that stream_cancelled deltas pass
// the ctx-cancelled gate AND still get identity-stamped, while other delta
// types are dropped once the context is cancelled.
func TestWriteStreamingDelta_CancelledGate(t *testing.T) {
	hub := &captureHub{}
	a := &CallLLMActivity{hub: hub}

	state := &streamProcessingState{messageID: "msg-1", streamSeq: 41}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	// Non-cancel delta is dropped on a cancelled context.
	a.writeStreamingDelta(cancelled, "chat-1", "content_block_delta", map[string]interface{}{
		"delta": "late text",
	}, state)
	require.Empty(t, hub.captured(), "content deltas must be dropped after cancellation")

	// stream_cancelled passes the gate and carries identity.
	a.writeStreamingDelta(cancelled, "chat-1", "stream_cancelled", map[string]interface{}{
		"reason": "user_cancelled",
	}, state)

	deltas := hub.captured()
	require.Len(t, deltas, 1)
	assert.Equal(t, streaming.DeltaTypeStreamCancelled, deltas[0].DeltaType)
	assert.Equal(t, "msg-1", deltas[0].MessageID)
	assert.Equal(t, int64(42), deltas[0].StreamSeq)
}

// TestWriteStreamingDelta_NilState pins that non-message deltas (emitted with
// a nil state, e.g. mcp_warning before streaming starts) stay id-less.
func TestWriteStreamingDelta_NilState(t *testing.T) {
	hub := &captureHub{}
	a := &CallLLMActivity{hub: hub}

	a.writeStreamingDelta(context.Background(), "chat-1", "mcp_warning", map[string]interface{}{
		"message": "server down",
	}, nil)

	deltas := hub.captured()
	require.Len(t, deltas, 1)
	assert.Empty(t, deltas[0].MessageID)
	assert.Zero(t, deltas[0].StreamSeq)
}

// Compile-time check that the test hub satisfies the interface.
var _ streaming.StreamingHub = (*captureHub)(nil)
