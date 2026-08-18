// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
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

type cancellingMockLLMDriver struct {
	ready chan struct{}
}

func (m *cancellingMockLLMDriver) Name() string { return "mock" }
func (m *cancellingMockLLMDriver) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}
func (m *cancellingMockLLMDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return nil, nil
}
func (m *cancellingMockLLMDriver) StreamResponse(ctx context.Context, _ []string, _ []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 2)
	ch <- llm.DriverEvent{Type: llm.EventContentStart}
	ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "partial answer"}
	close(m.ready)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}
func (m *cancellingMockLLMDriver) ValidateKey(context.Context) error { return nil }

// An interrupted turn PERSISTS its partial rather than handing it back.
//
// This used to assert the opposite: that the activity returned a CanceledError
// carrying the partial as cancellation details, which the workflow harvested.
// That channel only worked because the workflow WAITED for the cancelled
// activity to return (WaitForCancellation), and that wait is what made every
// stop take 1-3s -- a cancelled activity's return only reaches the worker on a
// heartbeat. The wait is gone, so the details channel is gone with it: the
// activity writes the row itself, on a context detached from the cancellation.
//
// The durable row is now the contract. Its identity comes from the step's
// position in the graph (callLLMIdempotencyKey), so the re-dispatch that
// follows an interrupt converges on this same row instead of adding a second
// assistant message for one turn.
func TestCallLLM_CancelledStreamPersistsPartialTurn(t *testing.T) {
	driver := &cancellingMockLLMDriver{ready: make(chan struct{})}
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	})

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	hub := &captureHub{}
	activityInstance := NewCallLLMActivity(h.Repo(), hub, nil, &staticConfigProvider{}, resolver, nil)

	input := callLLMInput(chat.ID, chat.ID, "mock-model")
	runCancelled := func(actCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
		cancelCtx, cancel := context.WithCancel(actCtx)
		defer cancel()
		go func() {
			<-driver.ready
			cancel()
		}()
		return activityInstance.Execute(cancelCtx, input)
	}

	h.env.RegisterActivity(runCancelled)
	_, err := h.env.ExecuteActivity(runCancelled, input)
	require.NoError(t, err, "an interrupted turn is not an activity failure any more")

	// The partial must exist in the transcript, not just in a payload that the
	// SDK would have thrown away.
	msgs, err := h.Repo().ListMessages(ctx, chat.ID, db.MessageListOptions{})
	require.NoError(t, err)

	var partial string
	for _, m := range msgs {
		if m.Role != reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			continue
		}
		blocks, err := h.Repo().ListContentBlocks(ctx, m.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.Content != nil {
				partial += *b.Content
			}
		}
	}
	assert.Contains(t, partial, "partial answer",
		"the text the user watched stream in must survive the interrupt")

	var sawCancelled bool
	for _, delta := range hub.captured() {
		if delta.DeltaType == streaming.DeltaTypeStreamCancelled {
			sawCancelled = true
		}
	}
	assert.True(t, sawCancelled, "UI must still be told the stream stopped")
}

// Compile-time check that the test hub satisfies the interface.
var _ streaming.StreamingHub = (*captureHub)(nil)
