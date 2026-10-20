// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// scriptedClient streams a fixed list of events and records the span context
// it was called under, so a test can check the HTTP layer would nest under
// the gen_ai span.
type scriptedClient struct {
	events  []llm.DriverEvent
	sawSpan trace.SpanContext
}

func (c *scriptedClient) Name() string                      { return "anthropic" }
func (c *scriptedClient) ValidateKey(context.Context) error { return nil }
func (c *scriptedClient) SendMessages(ctx context.Context, _ []string, _ []message.Message, _ []tools.Tool) (*llm.DriverResponse, error) {
	c.sawSpan = trace.SpanContextFromContext(ctx)
	for _, ev := range c.events {
		switch ev.Type {
		case llm.EventComplete:
			return ev.Response, nil
		case llm.EventError:
			return nil, ev.Error
		}
	}
	return nil, nil
}

func (c *scriptedClient) StreamResponse(ctx context.Context, _ []string, _ []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	c.sawSpan = trace.SpanContextFromContext(ctx)
	ch := make(chan llm.DriverEvent, len(c.events))
	for _, ev := range c.events {
		ch <- ev
	}
	close(ch)
	return ch
}

func recordGenAISpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func genAIAttrs(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	out := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes() {
		out[kv.Key] = kv.Value
	}
	return out
}

func endedSpans(t *testing.T, rec *tracetest.SpanRecorder, n int) []sdktrace.ReadOnlySpan {
	t.Helper()
	require.Eventually(t, func() bool { return len(rec.Ended()) >= n }, 5*time.Second, 5*time.Millisecond)
	return rec.Ended()
}

// Every streamed model call is a gen_ai span with model, tokens and finish
// reason; the driver runs under it; every event is still relayed, in order.
func TestBaseDriverStream_GenAISpan(t *testing.T) {
	rec := recordGenAISpans(t)
	client := &scriptedClient{events: []llm.DriverEvent{
		{Type: llm.EventContentDelta, Content: "hi"},
		{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Attempt: 1, StatusCode: 529, Reason: "http_529"}},
		{Type: llm.EventComplete, Response: &llm.DriverResponse{
			FinishReason: message.FinishReasonToolUse,
			Usage: llm.TokenUsage{
				InputTokens: 1200, OutputTokens: 340, ReasoningTokens: 100, CacheReadInputTokens: 1000,
			},
			ToolCalls: []message.ToolCall{{ID: "t1"}},
		}},
	}}
	d := &baseDriver{client: client, model: models.Model{ID: "claude-4.8-opus", APIModel: "claude-opus-4-8"}}

	var got []llm.EventType
	for ev := range d.StreamResponse(context.Background(), nil, nil, nil) {
		got = append(got, ev.Type)
	}
	assert.Equal(t, []llm.EventType{llm.EventContentDelta, llm.EventRetryWait, llm.EventComplete}, got)

	s := endedSpans(t, rec, 1)[0]
	assert.Equal(t, "chat claude-opus-4-8", s.Name())
	assert.Equal(t, trace.SpanKindClient, s.SpanKind())
	assert.Equal(t, s.SpanContext().SpanID(), client.sawSpan.SpanID(), "the driver runs under the gen_ai span")
	a := genAIAttrs(s)
	assert.Equal(t, "anthropic", a["gen_ai.provider.name"].AsString())
	assert.Equal(t, "claude-opus-4-8", a["gen_ai.request.model"].AsString())
	assert.Equal(t, "claude-opus-4-8", a["gen_ai.response.model"].AsString())
	assert.Equal(t, int64(1200), a["gen_ai.usage.input_tokens"].AsInt64())
	assert.Equal(t, int64(340), a["gen_ai.usage.output_tokens"].AsInt64())
	assert.Equal(t, int64(1000), a["gen_ai.usage.cache_read.input_tokens"].AsInt64())
	assert.Equal(t, int64(100), a["gen_ai.usage.reasoning.output_tokens"].AsInt64())
	assert.Equal(t, []string{"tool_use"}, a["gen_ai.response.finish_reasons"].AsStringSlice())
	assert.Equal(t, int64(1), a["gen_ai.retries"].AsInt64())
	require.Len(t, s.Events(), 1)
	assert.Equal(t, "gen_ai.retry_wait", s.Events()[0].Name)
}

func TestBaseDriverStream_GenAISpanErrorVsCancel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		wantError bool
	}{
		{"provider error sets error status", errors.New("overloaded"), true},
		{"cancellation is not an error", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := recordGenAISpans(t)
			d := &baseDriver{client: &scriptedClient{events: []llm.DriverEvent{{Type: llm.EventError, Error: tc.err}}}}
			for range d.StreamResponse(context.Background(), nil, nil, nil) {
			}
			s := endedSpans(t, rec, 1)[0]
			assert.Equal(t, tc.wantError, s.Status().Code == codes.Error)
			if !tc.wantError {
				assert.Equal(t, "canceled", genAIAttrs(s)["error.type"].AsString())
			}
		})
	}
}

// A consumer that walks away still ends the span (the relay gives up after
// the grace and drains).
func TestBaseDriverStream_GenAISpanEndsWhenTheConsumerLeaves(t *testing.T) {
	shortGrace(t)
	rec := recordGenAISpans(t)
	client := &blockingSendClient{events: 50, finished: make(chan struct{})}
	d := &baseDriver{client: client, model: models.Model{ID: "m"}}
	ctx, cancel := context.WithCancel(context.Background())
	<-d.StreamResponse(ctx, nil, nil, nil)
	cancel()
	endedSpans(t, rec, 1)
}

func TestBaseDriverSendMessages_GenAISpan(t *testing.T) {
	rec := recordGenAISpans(t)
	d := &baseDriver{
		client: &scriptedClient{events: []llm.DriverEvent{{Type: llm.EventComplete, Response: &llm.DriverResponse{Usage: llm.TokenUsage{InputTokens: 5, OutputTokens: 7}}}}},
		model:  models.Model{APIModel: "gpt-5.5"},
	}
	_, err := d.SendMessages(context.Background(), nil, nil, nil)
	require.NoError(t, err)
	a := genAIAttrs(endedSpans(t, rec, 1)[0])
	assert.Equal(t, int64(7), a["gen_ai.usage.output_tokens"].AsInt64())
	assert.Equal(t, "gpt-5.5", a["gen_ai.response.model"].AsString())
	assert.False(t, a["gen_ai.request.stream"].AsBool())
}
