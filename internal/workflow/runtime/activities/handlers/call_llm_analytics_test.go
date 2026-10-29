// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// usageDriver finishes one streamed call with the usage it was given, or fails
// it with err. Its reply text is a marker the event must never carry.
type usageDriver struct {
	usage llm.TokenUsage
	err   error
}

const usageDriverReply = "SECRET-REPLY-TEXT"

func (d *usageDriver) Name() string                          { return "anthropic" }
func (d *usageDriver) Model() models.Model                   { return models.Model{ID: "claude-test"} }
func (d *usageDriver) ValidateKey(ctx context.Context) error { return nil }

func (d *usageDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{Content: usageDriverReply}, nil
}

func (d *usageDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 1)
	if d.err != nil {
		ch <- llm.DriverEvent{Type: llm.EventError, Error: d.err}
	} else {
		ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{
			Content:      usageDriverReply,
			FinishReason: message.FinishReasonEndTurn,
			Usage:        d.usage,
		}}
	}
	close(ch)
	return ch
}

func newAnalyticsCallLLM(t *testing.T, h *IdempotencyTestHelper, driver llm.Driver, rec *productEventRecorder) *CallLLMActivity {
	t.Helper()
	activity := NewCallLLMActivity(
		h.Repo(), nil, nil, &staticConfigProvider{},
		func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) { return driver, nil },
		nil,
	)
	activity.trackerFor = rec.trackerFor
	return activity
}

// Every model call that finishes emits product.llm_call_completed. The event
// disappeared on 2026-10-24 when #1360 deleted its only emitter; this pins the
// call site, not just the Tracker method.
func TestCallLLM_EmitsLLMCallCompletedEvent(t *testing.T) {
	t.Parallel()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-llm-evt", "user-llm-evt")
	chat := h.CreateTestChat(ctx, "chat-llm-evt", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	rec := newProductEventRecorder()
	activity := newAnalyticsCallLLM(t, h, &usageDriver{usage: llm.TokenUsage{
		TokenCount:               1500,
		InputTokens:              1000,
		OutputTokens:             200,
		CacheReadInputTokens:     300,
		CacheCreationInputTokens: 40,
	}}, rec)

	var output reliantv1.CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, buildResponseToolInput(chat.ID, nil), &output))

	events := rec.events(t, "product.llm_call_completed")
	require.Len(t, events, 1, "one model call emits exactly one event")
	evt := events[0]
	assert.Equal(t, project.UserID, evt["user.id"])
	assert.Equal(t, "backend", evt["app.client"])
	assert.Equal(t, "anthropic", evt["product.provider"])
	assert.Equal(t, "claude-test", evt["product.model"])
	assert.Equal(t, float64(1000), evt["product.input_tokens"])
	assert.Equal(t, float64(200), evt["product.output_tokens"])
	assert.Equal(t, float64(300), evt["product.cache_read_tokens"])
	assert.Equal(t, float64(40), evt["product.cache_write_tokens"])
	assert.Equal(t, true, evt["product.success"])
	assert.Equal(t, true, evt["product.is_streaming"])
	assert.Equal(t, chat.ID, evt["product.chat_id"])
	assert.Equal(t, "route__routing_decision", evt["product.step_name"])
	assert.Contains(t, evt, "product.latency_ms")
	assert.NotContains(t, evt, "product.error_type")

	// Counts and metadata only: nothing the user or the model wrote.
	for key, value := range evt {
		assert.NotContains(t, key, "content")
		assert.NotContains(t, key, "text")
		assert.NotEqual(t, usageDriverReply, value, key)
	}
}

func TestCallLLM_FailedCallEmitsLLMCallCompletedWithErrorType(t *testing.T) {
	t.Parallel()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-llm-evt-fail", "user-llm-evt-fail")
	chat := h.CreateTestChat(ctx, "chat-llm-evt-fail", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	rec := newProductEventRecorder()
	activity := newAnalyticsCallLLM(t, h, &usageDriver{err: errors.New("429 rate limit exceeded")}, rec)

	var output reliantv1.CallLLMOutput
	_ = h.ExecuteActivity(activity.Execute, buildResponseToolInput(chat.ID, nil), &output)

	events := rec.events(t, "product.llm_call_completed")
	require.Len(t, events, 1)
	assert.Equal(t, false, events[0]["product.success"])
	assert.Equal(t, "rate_limit", events[0]["product.error_type"])
}

func TestClassifyLLMError(t *testing.T) {
	for err, want := range map[string]string{
		"429 too many requests":              "rate_limit",
		"maximum context length is 200000":   "context_length",
		"401 invalid api key":                "auth",
		"context deadline exceeded":          "timeout",
		"http2: client connection lost":      "network",
		"something nobody anticipated":       "unknown",
		"operation canceled by the operator": "cancelled",
	} {
		assert.Equal(t, want, classifyLLMError(errors.New(err)), err)
	}
	assert.Empty(t, classifyLLMError(nil))
}
