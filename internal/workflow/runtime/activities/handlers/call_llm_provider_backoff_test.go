// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
)

// The driver announces a provider retry before it sleeps; this is the wiring
// that turns the announcement into the durable marker every supervision surface
// reads. Without it the announcement dies inside the activity and the run is
// still invisible while it waits — the retry ladder runs inside a single
// Temporal activity attempt, so nothing else it touches changes.
func TestProcessStreamEvent_RetryWaitWritesAndClearsTheBackoffMarker(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	a := &CallLLMActivity{repo: h.Repo()}
	state := &streamProcessingState{blockStates: NewBlockStreamState()}

	require.NoError(t, a.processStreamEvent(ctx, "chat-1", "thread-1", llm.DriverEvent{
		Type: llm.EventRetryWait,
		Retry: &llm.RetryWait{
			Attempt: 3, MaxAttempts: 8, Delay: 9600 * time.Millisecond,
			StatusCode: 429, Reason: "http_429",
		},
	}, state))

	marks, err := h.Repo().ProviderBackoffByChat(ctx, "chat-1")
	require.NoError(t, err)
	b := marks["thread-1"]
	require.True(t, b.Waiting(), "a retrying thread must be marked as parked, not left looking like it is working")
	require.Equal(t, int64(3), b.Attempt)
	require.Equal(t, int64(8), b.MaxAttempts)
	require.Equal(t, int64(429), b.StatusCode)
	require.Equal(t, "http_429", b.Reason)
	require.True(t, b.ResumeAt.After(b.WaitingSince), "the marker must carry the wait it declared, so it cannot outlive it")

	// Any other event is proof the provider answered: the marker is released
	// once, and what the thread lost survives it.
	require.NoError(t, a.processStreamEvent(ctx, "chat-1", "thread-1", llm.DriverEvent{
		Type: llm.EventContentDelta, Content: "hello",
	}, state))

	marks, err = h.Repo().ProviderBackoffByChat(ctx, "chat-1")
	require.NoError(t, err)
	b = marks["thread-1"]
	require.False(t, b.Waiting(), "the marker outlived the wait: a working thread would report as parked")
	require.Equal(t, int64(1), b.Retries, "the cumulative cost must survive the wait for post-hoc forensics")
	require.False(t, state.inProviderBackoff)
}
