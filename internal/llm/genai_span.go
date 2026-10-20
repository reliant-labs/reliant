// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// GenAISpan is the span for one provider call, carrying the OpenTelemetry
// gen_ai semantic conventions: "chat <model>", with the provider, the
// requested and served model, input/output/cache/reasoning token counts and
// the finish reason. The call runs under it, so the driver's HTTP spans
// (otelhttp, inside the transport) — retries included — are its children.
//
// Before it, a model call was visible on a trace only as otelhttp's HTTP POST:
// which model it was and what it cost were not on the trace at all.
//
// It is opened by the layer that already relays every driver's events (the
// base driver in internal/llm/drivers), not by a wrapper of its own: a second
// relay between the driver and its consumer moves events the driver has
// already produced out of the consumer's reach when it is cancelled, and
// CallLLM persists the partial turn from exactly those events.
type GenAISpan struct {
	span      trace.Span
	requested string
	retries   int
}

// StartGenAISpan opens the span for a call to model through provider (the
// driver family: anthropic, openai, codex, ...). The returned ctx carries it;
// pass that ctx to the provider call. End it exactly once.
func StartGenAISpan(ctx context.Context, provider string, model models.Model, stream bool) (context.Context, *GenAISpan) {
	requested := model.APIModel
	if requested == "" {
		requested = string(model.ID)
	}
	name := "chat"
	if requested != "" {
		name += " " + requested
	}
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", requested),
		attribute.Bool("gen_ai.request.stream", stream),
	}
	if model.ID != "" {
		attrs = append(attrs, attribute.String("reliant.model_id", string(model.ID)))
	}
	// Resolved per call, not cached: a package-level tracer taken before the
	// process installs its provider would stay a no-op.
	ctx, span := otel.Tracer("reliant.llm").Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
	return ctx, &GenAISpan{span: span, requested: requested}
}

// Observe records what a streamed event says about the call: the response
// and its usage, an error, a backoff wait.
func (s *GenAISpan) Observe(ev DriverEvent) {
	switch ev.Type {
	case EventComplete:
		if ev.Response != nil {
			s.RecordResponse(ev.Response, ev.Model.APIModel)
		}
	case EventError:
		s.RecordError(ev.Error)
	case EventRetryWait:
		s.retries++
		if ev.Retry != nil {
			s.span.AddEvent("gen_ai.retry_wait", trace.WithAttributes(
				attribute.Int("attempt", ev.Retry.Attempt),
				attribute.Int("http.response.status_code", ev.Retry.StatusCode),
				attribute.String("reason", ev.Retry.Reason),
				attribute.Int64("delay_ms", ev.Retry.Delay.Milliseconds()),
			))
		}
	}
}

// RecordResponse sets the response attributes: served model (the requested
// one when servedModel is empty), token usage, finish reason, upstream id.
func (s *GenAISpan) RecordResponse(resp *DriverResponse, servedModel string) {
	if resp == nil {
		return
	}
	u := resp.Usage
	attrs := []attribute.KeyValue{
		attribute.Int64("gen_ai.usage.input_tokens", u.InputTokens),
		attribute.Int64("gen_ai.usage.output_tokens", u.OutputTokens),
	}
	if servedModel == "" {
		servedModel = s.requested
	}
	if servedModel != "" {
		attrs = append(attrs, attribute.String("gen_ai.response.model", servedModel))
	}
	if resp.FinishReason != "" {
		attrs = append(attrs, attribute.StringSlice("gen_ai.response.finish_reasons", []string{string(resp.FinishReason)}))
	}
	if u.CacheReadInputTokens > 0 {
		attrs = append(attrs, attribute.Int64("gen_ai.usage.cache_read.input_tokens", u.CacheReadInputTokens))
	}
	if u.CacheCreationInputTokens > 0 {
		attrs = append(attrs, attribute.Int64("gen_ai.usage.cache_creation.input_tokens", u.CacheCreationInputTokens))
	}
	if u.ReasoningTokens > 0 {
		attrs = append(attrs, attribute.Int64("gen_ai.usage.reasoning.output_tokens", u.ReasoningTokens))
	}
	if u.Cost > 0 {
		attrs = append(attrs, attribute.Float64("reliant.cost_usd", u.Cost))
	}
	if resp.UpstreamRequestID != "" {
		attrs = append(attrs, attribute.String("gen_ai.response.id", resp.UpstreamRequestID))
	}
	if len(resp.ToolCalls) > 0 {
		attrs = append(attrs, attribute.Int("reliant.tool_calls", len(resp.ToolCalls)))
	}
	s.span.SetAttributes(attrs...)
}

// RecordError records err on the span. A cancellation is the caller hanging
// up (the user pressed stop, a newer turn superseded this one), not a
// provider failure, so it sets no error status.
func (s *GenAISpan) RecordError(err error) {
	if err == nil {
		return
	}
	s.span.RecordError(err)
	if errors.Is(err, context.Canceled) {
		s.span.SetAttributes(attribute.String("error.type", "canceled"))
		return
	}
	s.span.SetStatus(codes.Error, err.Error())
}

// End ends the span.
func (s *GenAISpan) End() {
	if s.retries > 0 {
		s.span.SetAttributes(attribute.Int("gen_ai.retries", s.retries))
	}
	s.span.End()
}
