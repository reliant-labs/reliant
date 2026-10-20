// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"errors"
	"testing"

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

type scriptedDriver struct {
	model  models.Model
	events []DriverEvent
	// sawSpan records the span context the driver was called with, so the
	// test can check the HTTP layer would nest under the gen_ai span.
	sawSpan trace.SpanContext
}

func (d *scriptedDriver) Name() string                      { return "scripted" }
func (d *scriptedDriver) Model() models.Model               { return d.model }
func (d *scriptedDriver) ValidateKey(context.Context) error { return nil }
func (d *scriptedDriver) SendMessages(ctx context.Context, _ []string, _ []message.Message, _ []tools.Tool) (*DriverResponse, error) {
	d.sawSpan = trace.SpanContextFromContext(ctx)
	for _, ev := range d.events {
		if ev.Type == EventComplete {
			return ev.Response, nil
		}
		if ev.Type == EventError {
			return nil, ev.Error
		}
	}
	return nil, nil
}
func (d *scriptedDriver) StreamResponse(ctx context.Context, _ []string, _ []message.Message, _ []tools.Tool) <-chan DriverEvent {
	d.sawSpan = trace.SpanContextFromContext(ctx)
	ch := make(chan DriverEvent, len(d.events))
	for _, ev := range d.events {
		ch <- ev
	}
	close(ch)
	return ch
}

func recordLLMSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func spanAttrs(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	out := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes() {
		out[kv.Key] = kv.Value
	}
	return out
}

func TestTracedStreamRecordsGenAIAttributes(t *testing.T) {
	rec := recordLLMSpans(t)
	inner := &scriptedDriver{
		model: models.Model{ID: "claude-4.8-opus", APIModel: "claude-opus-4-8"},
		events: []DriverEvent{
			{Type: EventContentDelta, Content: "hi"},
			{Type: EventRetryWait, Retry: &RetryWait{Attempt: 1, StatusCode: 529, Reason: "http_529"}},
			{Type: EventComplete, Response: &DriverResponse{
				FinishReason: message.FinishReasonToolUse,
				Usage: TokenUsage{
					InputTokens: 1200, OutputTokens: 340, ReasoningTokens: 100,
					CacheReadInputTokens: 1000,
				},
				ToolCalls: []message.ToolCall{{ID: "t1"}},
			}},
		},
	}
	d := Traced(inner, "anthropic")

	var got []EventType
	for ev := range d.StreamResponse(context.Background(), nil, nil, nil) {
		got = append(got, ev.Type)
	}
	assert.Equal(t, []EventType{EventContentDelta, EventRetryWait, EventComplete}, got, "every event is forwarded, in order")

	spans := rec.Ended()
	require.Len(t, spans, 1)
	s := spans[0]
	assert.Equal(t, "chat claude-opus-4-8", s.Name())
	assert.Equal(t, trace.SpanKindClient, s.SpanKind())
	assert.Equal(t, s.SpanContext().SpanID(), inner.sawSpan.SpanID(), "the driver runs under the gen_ai span")
	a := spanAttrs(s)
	assert.Equal(t, "chat", a["gen_ai.operation.name"].AsString())
	assert.Equal(t, "anthropic", a["gen_ai.provider.name"].AsString())
	assert.Equal(t, "claude-opus-4-8", a["gen_ai.request.model"].AsString())
	assert.Equal(t, int64(1200), a["gen_ai.usage.input_tokens"].AsInt64())
	assert.Equal(t, int64(340), a["gen_ai.usage.output_tokens"].AsInt64())
	assert.Equal(t, int64(1000), a["gen_ai.usage.cache_read.input_tokens"].AsInt64())
	assert.Equal(t, int64(100), a["gen_ai.usage.reasoning.output_tokens"].AsInt64())
	assert.Equal(t, []string{"tool_use"}, a["gen_ai.response.finish_reasons"].AsStringSlice())
	assert.Equal(t, int64(1), a["gen_ai.retries"].AsInt64())
	require.Len(t, s.Events(), 1)
	assert.Equal(t, "gen_ai.retry_wait", s.Events()[0].Name)
}

func TestTracedStreamErrorSetsStatus(t *testing.T) {
	rec := recordLLMSpans(t)
	d := Traced(&scriptedDriver{events: []DriverEvent{{Type: EventError, Error: errors.New("overloaded")}}}, "openai")
	for range d.StreamResponse(context.Background(), nil, nil, nil) {
	}
	s := rec.Ended()[0]
	assert.Equal(t, codes.Error, s.Status().Code)
}

func TestTracedCancellationIsNotAnError(t *testing.T) {
	rec := recordLLMSpans(t)
	d := Traced(&scriptedDriver{events: []DriverEvent{{Type: EventError, Error: context.Canceled}}}, "openai")
	for range d.StreamResponse(context.Background(), nil, nil, nil) {
	}
	s := rec.Ended()[0]
	assert.NotEqual(t, codes.Error, s.Status().Code)
	assert.Equal(t, "canceled", spanAttrs(s)["error.type"].AsString())
}

// An abandoned stream (the consumer cancelled ctx and stopped reading) still
// ends its span and does not leak the forwarding goroutine.
func TestTracedStreamAbandoned(t *testing.T) {
	rec := recordLLMSpans(t)
	ctx, cancel := context.WithCancel(context.Background())
	events := make([]DriverEvent, 10)
	for i := range events {
		events[i] = DriverEvent{Type: EventContentDelta}
	}
	ch := Traced(&scriptedDriver{events: events}, "anthropic").StreamResponse(ctx, nil, nil, nil)
	<-ch
	cancel()
	require.Eventually(t, func() bool { return len(rec.Ended()) == 1 }, 5e9, 1e7)
}

func TestTracedSendMessages(t *testing.T) {
	rec := recordLLMSpans(t)
	d := Traced(&scriptedDriver{
		model:  models.Model{APIModel: "gpt-5.5"},
		events: []DriverEvent{{Type: EventComplete, Response: &DriverResponse{Usage: TokenUsage{InputTokens: 5, OutputTokens: 7}}}},
	}, "openai")
	_, err := d.SendMessages(context.Background(), nil, nil, nil)
	require.NoError(t, err)
	a := spanAttrs(rec.Ended()[0])
	assert.Equal(t, int64(7), a["gen_ai.usage.output_tokens"].AsInt64())
	assert.False(t, a["gen_ai.request.stream"].AsBool())
}

func TestTracedIsIdempotent(t *testing.T) {
	d := Traced(&scriptedDriver{}, "x")
	assert.Same(t, d, Traced(d, "x"))
	assert.Nil(t, Traced(nil, "x"))
}
