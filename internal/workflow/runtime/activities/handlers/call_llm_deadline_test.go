// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An expired activity DEADLINE is not a user interrupt.
//
// streamLLMResponse treated every streamCtx expiry as one, which set
// streamInterrupted and skipped all stream-error reporting — so a turn whose
// Temporal StartToClose deadline elapsed mid-stream returned a completed,
// EMPTY turn with a nil error. The agent loop reads no-output-no-error as "the
// model finished", which is the silent-truncation signature the cancel path
// already carries a long comment about.
//
// The progress timeout is set far above the deadline here so the deadline is
// unambiguously what ends the stream; this is the deadline path, not the
// stalled-stream path its sibling test covers.
func TestCallLLM_DeadlineExceededFailsRatherThanCompleting(t *testing.T) {
	t.Setenv("RELIANT_LLM_STREAM_PROGRESS_TIMEOUT", "5m")

	driver := &stalledProgressDriver{stopped: make(chan struct{})}
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

	run := func(activityCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
		boundedCtx, cancel := context.WithTimeout(activityCtx, 300*time.Millisecond)
		defer cancel()
		return activityInstance.Execute(boundedCtx, input)
	}
	h.env.RegisterActivity(run)

	_, err := h.env.ExecuteActivity(run, callLLMInput(chat.ID, chat.ID, "mock-model"))
	require.Error(t, err, "an expired deadline must fail the turn so Temporal retries it, not return a completed empty turn")
	assert.Contains(t, err.Error(), "deadline", "the error should name the deadline as the cause")
	assert.NotContains(t, err.Error(), "cancelled by user",
		"a deadline is not a user cancellation and must not be reported as one")
}
