// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Malformed model output gets exactly one more attempt: the first attempt
// that fails this way is retried, every later one ends the turn, so a model
// that keeps producing the same malformed call cannot loop.
func TestMalformedOutputError_RetriesOnce(t *testing.T) {
	t.Parallel()
	cause := fmt.Errorf("Gemini stopped with MALFORMED_FUNCTION_CALL: %w", drivererrors.ErrMalformedModelOutput)
	for _, tc := range []struct {
		attempt int32
		delay   time.Duration
		retry   bool
	}{
		{0, malformedOutputBackoff, true}, // outside an activity
		{1, malformedOutputBackoff, true},
		{2, 0, false},
		{5, 0, false},
	} {
		err := &malformedOutputError{cause: cause, provider: "gemini", model: "gemini-3-pro", attempt: tc.attempt}
		delay, retry := err.RetryAfter()
		assert.Equal(t, tc.retry, retry, "attempt %d", tc.attempt)
		assert.Equal(t, tc.delay, delay, "attempt %d", tc.attempt)
	}
	err := &malformedOutputError{cause: cause, provider: "gemini", model: "gemini-3-pro", attempt: 1}
	assert.Equal(t, "ProviderMalformedOutput", err.TemporalErrorType())
	assert.ErrorIs(t, err, drivererrors.ErrMalformedModelOutput)
	assert.Equal(t, "gemini-3-pro on gemini returned a malformed response: Gemini stopped with MALFORMED_FUNCTION_CALL: the model produced a malformed response", err.Error())
}

// malformedDriver ends its stream the way a Gemini driver does on
// MALFORMED_FUNCTION_CALL.
type malformedDriver struct {
	mockLLMDriverForIdempotency
}

func (d *malformedDriver) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	events := make(chan llm.DriverEvent, 1)
	events <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("Gemini stopped with MALFORMED_FUNCTION_CALL: %w", drivererrors.ErrMalformedModelOutput)}
	close(events)
	return events
}

// CallLLM fails the turn with the self-scheduling malformedOutputError, which
// the activity boundary turns into one retry.
func TestCallLLM_MalformedOutputFailsWithSelfSchedulingError(t *testing.T) {
	t.Parallel()

	driver := &malformedDriver{}
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	})
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	activityInstance := NewCallLLMActivity(CallLLMDeps{Store: h.Repo(), Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}), Spawns: store.NewToolCallExtStore(h.Repo().ORM()), Hub: &captureHub{}, ToolsFactory: nil, ConfigProvider: &staticConfigProvider{}, DriverResolver: resolver, MCPBinder: nil})

	var returned error
	run := func(activityCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
		output, err := activityInstance.Execute(activityCtx, input)
		returned = err
		return output, err
	}
	h.env.RegisterActivity(run)
	_, err := h.env.ExecuteActivity(run, callLLMInput(chat.ID, chat.ID, "mock-model"))
	require.Error(t, err)
	require.Error(t, returned)

	var malformed *malformedOutputError
	require.ErrorAs(t, returned, &malformed, "the activity boundary schedules the retry from this")
	assert.ErrorIs(t, returned, drivererrors.ErrMalformedModelOutput)
	assert.Contains(t, returned.Error(), "mock-model on ")
	assert.Contains(t, returned.Error(), "returned a malformed response: ")
	assert.Contains(t, returned.Error(), "Gemini stopped with MALFORMED_FUNCTION_CALL")
	_, retry := malformed.RetryAfter()
	assert.True(t, retry, "the first attempt is retried")
}
