// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Traced wraps d so every provider call is a span carrying the OpenTelemetry
// gen_ai semantic conventions: "chat <model>", with the provider, the
// requested and served model, input/output/cache/reasoning token counts and
// the finish reason. The driver's HTTP spans (otelhttp, inside the transport)
// become its children, so a trace shows the model call as one unit with the
// network attempts — retries included — under it.
//
// Before this, a model call was visible only as an otelhttp "HTTP POST" span:
// which model it was and what it cost were not on the trace at all.
//
// provider is the driver id (anthropic, openai, codex, ...): it is what the
// request was routed to, which d.Name() does not always say.
func Traced(d Driver, provider string) Driver {
	if d == nil {
		return nil
	}
	if _, ok := d.(*tracedDriver); ok {
		return d
	}
	return &tracedDriver{Driver: d, provider: provider}
}

type tracedDriver struct {
	Driver
	provider string
}

func (t *tracedDriver) start(ctx context.Context, stream bool) (context.Context, trace.Span) {
	model := t.Model()
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
		attribute.String("gen_ai.provider.name", t.provider),
		attribute.String("gen_ai.request.model", requested),
		attribute.Bool("gen_ai.request.stream", stream),
	}
	if model.ID != "" {
		attrs = append(attrs, attribute.String("reliant.model_id", string(model.ID)))
	}
	// Resolved per call, not cached: a package-level tracer taken before the
	// process installs its provider would stay a no-op.
	return otel.Tracer("reliant.llm").Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
}

func (t *tracedDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tls []tools.Tool) (*DriverResponse, error) {
	ctx, span := t.start(ctx, false)
	defer span.End()
	resp, err := t.Driver.SendMessages(ctx, prompts, messages, tls)
	if resp != nil {
		recordResponse(span, resp, t.Model().APIModel)
	}
	recordError(span, err)
	return resp, err
}

func (t *tracedDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tls []tools.Tool) <-chan DriverEvent {
	ctx, span := t.start(ctx, true)
	in := t.Driver.StreamResponse(ctx, prompts, messages, tls)
	out := make(chan DriverEvent)
	go func() {
		defer close(out)
		defer span.End()
		requested := t.Model().APIModel
		var retries int
		for ev := range in {
			switch ev.Type {
			case EventComplete:
				if ev.Response != nil {
					served := requested
					if ev.Model.APIModel != "" {
						served = ev.Model.APIModel
					}
					recordResponse(span, ev.Response, served)
				}
			case EventError:
				recordError(span, ev.Error)
			case EventRetryWait:
				retries++
				if ev.Retry != nil {
					span.AddEvent("gen_ai.retry_wait", trace.WithAttributes(
						attribute.Int("attempt", ev.Retry.Attempt),
						attribute.Int("http.response.status_code", ev.Retry.StatusCode),
						attribute.String("reason", ev.Retry.Reason),
						attribute.Int64("delay_ms", ev.Retry.Delay.Milliseconds()),
					))
				}
			}
			// The consumer owns the pace: a blocked send here is the same
			// backpressure the driver's own channel applied. If the consumer
			// abandons the stream it cancels ctx, which ends the driver and so
			// closes in.
			select {
			case out <- ev:
			case <-ctx.Done():
				// Keep draining so the driver's goroutine can exit.
				for range in {
				}
				recordError(span, ctx.Err())
				return
			}
		}
		if retries > 0 {
			span.SetAttributes(attribute.Int("gen_ai.retries", retries))
		}
	}()
	return out
}

func recordResponse(span trace.Span, resp *DriverResponse, servedModel string) {
	u := resp.Usage
	attrs := []attribute.KeyValue{
		attribute.Int64("gen_ai.usage.input_tokens", u.InputTokens),
		attribute.Int64("gen_ai.usage.output_tokens", u.OutputTokens),
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
	span.SetAttributes(attrs...)
}

func recordError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	// A cancellation is the caller hanging up (the user pressed stop, a newer
	// turn superseded this one), not a provider failure.
	if errors.Is(err, context.Canceled) {
		span.SetAttributes(attribute.String("error.type", "canceled"))
		return
	}
	span.SetStatus(codes.Error, err.Error())
}
