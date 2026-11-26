// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"

	"github.com/reliant-labs/forge/pkg/observe/genai"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
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
	span       trace.Span
	ctx        context.Context
	provider   string
	requested  string
	retries    int
	firstToken bool
}

// StartGenAISpan opens the span for a call to model through provider (the
// driver family: anthropic, openai, codex, ...). The returned ctx carries it;
// pass that ctx to the provider call. End it exactly once.
func StartGenAISpan(ctx context.Context, provider string, model models.Model, stream bool) (context.Context, *GenAISpan) {
	requested := model.APIModel
	if requested == "" {
		requested = string(model.ID)
	}
	// forge records gen_ai.request.stream only on a streamed call; reliant
	// has always said false too, and a reader filtering on it keeps working.
	extra := []attribute.KeyValue{attribute.Bool(genai.AttrRequestStream, stream)}
	if model.ID != "" {
		extra = append(extra, attribute.String(attrModelID, string(model.ID)))
	}
	// The run the call belongs to — workflow, node, agent, run, chat, user —
	// so a dashboard can read tokens by workflow without joining to the
	// workflow's span, which a continue-as-new or a resume puts on another
	// trace. The worker puts the scope on the activity's ctx
	// (temporalobs.NewRunScopeWorkerInterceptor); outside a run it is empty.
	ctx = genai.WithScope(ctx, telemetry.RunScopeFromContext(ctx).GenAIAttributes()...)
	ctx, span := genai.StartChat(ctx, genai.Chat{
		Provider:   provider,
		Model:      requested,
		Stream:     stream,
		Attributes: extra,
	})
	s := &GenAISpan{span: span, provider: provider, requested: requested}
	ctx = context.WithValue(ctx, genAISpanKey{}, s)
	s.ctx = ctx
	return ctx, s
}

// genAISpanKey is the context key of the call's GenAISpan, so a driver can
// add to its own call's span (RecordGatewayCallID) and never to whatever
// other span its ctx holds.
type genAISpanKey struct{}

// RecordGatewayCallID sets the LLM gateway's id for the call
// (telemetry.AttrGatewayCallID) on the chat span ctx carries, and on the
// call's GatewayCall when the caller asked for one (WithGatewayCall), so the
// usage ledger's row and the span name the same attempt. Only a driver that
// routes through the gateway calls it after each attempt, success or failure.
// A later nonempty id replaces an earlier one's on the span. An empty id clears GatewayCall so an
// unanswered attempt or missing header cannot inherit an earlier attempt's id.
// It adds no span attribute; a ctx with neither (a key validation) records nothing.
func RecordGatewayCallID(ctx context.Context, id string) {
	if g, ok := ctx.Value(gatewayCallKey{}).(*GatewayCall); ok {
		g.set(id)
	}
	if id == "" {
		return
	}
	if s, ok := ctx.Value(genAISpanKey{}).(*GenAISpan); ok {
		s.span.SetAttributes(attribute.String(telemetry.AttrGatewayCallID, id))
	}
}

// Reliant's own attributes of a model call, beside forge's.
const (
	// attrModelID is reliant's id for the model (models.Model.ID), which the
	// requested model (the provider's name for it) does not always equal.
	attrModelID = "reliant.model_id"
	// attrToolCalls is how many tool calls the response asked for.
	attrToolCalls = "reliant.tool_calls"
)

// Observe records what a streamed event says about the call: the response
// and its usage, an error, a backoff wait.
func (s *GenAISpan) Observe(ev DriverEvent) {
	switch ev.Type {
	case EventContentDelta, EventThinkingDelta:
		// The first token of the response: the span's start to this event
		// is the time to first token. Once per call, retries included.
		if !s.firstToken {
			s.firstToken = true
			genai.FirstToken(s.span)
		}
	case EventComplete:
		if ev.Response != nil {
			s.RecordResponse(ev.Response, ev.Model.APIModel)
		}
	case EventError:
		s.RecordError(ev.Error)
	case EventRetryWait:
		s.retries++
		if ev.Retry != nil {
			genai.RetryWait(s.span, genai.Retry{
				Attempt:    ev.Retry.Attempt,
				StatusCode: ev.Retry.StatusCode,
				Reason:     ev.Retry.Reason,
				Delay:      ev.Retry.Delay,
			})
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
	if servedModel == "" {
		servedModel = s.requested
	}
	r := genai.Response{
		Model: servedModel,
		ID:    resp.UpstreamRequestID,
		Usage: genai.Usage{
			InputTokens:              u.InputTokens,
			OutputTokens:             u.OutputTokens,
			CacheReadInputTokens:     u.CacheReadInputTokens,
			CacheCreationInputTokens: u.CacheCreationInputTokens,
			ReasoningOutputTokens:    u.ReasoningTokens,
		},
	}
	if resp.FinishReason != "" {
		r.FinishReasons = []string{string(resp.FinishReason)}
	}
	var extra []attribute.KeyValue
	if u.Cost > 0 {
		// The cost the provider or gateway reported: forge records it as
		// forge.ai.cost.usd, and reliant keeps the name it reported it under
		// first beside it.
		cost := u.Cost
		r.CostUSD = &cost
		extra = append(extra, attribute.Float64(telemetry.AttrReliantCostUSD, cost))
	}
	if len(resp.ToolCalls) > 0 {
		extra = append(extra, attribute.Int(attrToolCalls, len(resp.ToolCalls)))
	}
	genai.SetResponse(s.span, r)
	s.span.SetAttributes(extra...)
}

// RecordError records err's class on the span (ErrorType): error.type, and an
// error status described by the same code. A cancellation is the caller
// hanging up (the user pressed stop, a newer turn superseded this one), not a
// provider failure, so it sets no error status.
//
// The error's text is never on the span or in the log: a provider can echo
// anything into it, the prompt included. Only the error class and call metadata
// are logged on the call's context, alongside its run ids and trace.
func (s *GenAISpan) RecordError(err error) {
	if err == nil {
		return
	}
	errType := ErrorType(err)
	telemetry.SetAISpanError(s.span, err, errType)
	if errType == telemetry.ErrorTypeCanceled {
		return
	}
	logging.WarnContext(s.ctx, "LLM provider call failed",
		telemetry.AttrErrorType, errType,
		"provider", s.provider, "model", s.requested)
}

// End ends the span.
func (s *GenAISpan) End() {
	if s.retries > 0 {
		s.span.SetAttributes(attribute.Int(telemetry.AttrGenAIRetries, s.retries))
	}
	s.span.End()
}
