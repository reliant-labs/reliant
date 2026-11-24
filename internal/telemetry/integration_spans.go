// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// IntegrationCall is the span of one integration action call:
// "integration.call <integration>.<operation>". It is a child of whatever span
// ctx carries: the workflow.node span of an action node, or the execute_tool
// span of an agent's integration tool. It carries the run scope's attributes
// too (GenAIAttributes: org.id, the run and node, and the chat as
// gen_ai.conversation.id), so a call is found, and counted, by its org,
// workflow, run and node without walking the tree. A call made outside any run
// (a picker listing its options) carries what its caller put in the scope:
// the user, and the org when the caller knows one.
type IntegrationCall struct {
	span trace.Span
}

// StartIntegrationCall opens the span for one call of operation (an action
// id) of integration (a manifest id); uses is the reference it was resolved
// from, "" when there is none. attrs are added at the start (AttrIntegrationHosted).
// The returned ctx carries the span; end it exactly once with End.
func StartIntegrationCall(ctx context.Context, integration, operation, uses string, attrs ...attribute.KeyValue) (context.Context, *IntegrationCall) {
	all := append([]attribute.KeyValue{
		attribute.String(AttrIntegrationID, integration),
		attribute.String(AttrIntegrationOperation, operation),
	}, RunScopeFromContext(ctx).GenAIAttributes()...)
	if uses != "" {
		all = append(all, attribute.String(AttrIntegrationUses, uses))
	}
	all = append(all, attrs...)
	ctx, span := aiTracer().Start(ctx, SpanNameIntegrationCall+" "+integration+"."+operation,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(all...))
	return ctx, &IntegrationCall{span: span}
}

// End records how the call ended (an IntegrationOutcome* value, and the
// failure's code when it has one) plus attrs, and ends the span. Any outcome
// but ok and canceled sets the span's status to Error, described by the code
// alone: never by an error's text or the response, which may hold what the
// provider echoed back. A cancellation is error.type=canceled and no error
// status, as EndAISpan records it.
func (c *IntegrationCall) End(outcome, errorCode string, attrs ...attribute.KeyValue) {
	if c == nil {
		return
	}
	c.span.SetAttributes(attrs...)
	c.span.SetAttributes(attribute.String(AttrIntegrationOutcome, outcome))
	if errorCode != "" {
		c.span.SetAttributes(attribute.String(AttrIntegrationErrorCode, errorCode))
	}
	switch outcome {
	case IntegrationOutcomeOK:
	case IntegrationOutcomeCanceled:
		c.span.SetAttributes(attribute.String(AttrErrorType, ErrorTypeCanceled))
	default:
		description := errorCode
		if description == "" {
			description = outcome
		}
		c.span.SetAttributes(attribute.String(AttrErrorType, description))
		c.span.SetStatus(codes.Error, description)
	}
	c.span.End()
}

// IntegrationAnswerOutcome classifies a call the provider answered: ok, or
// error with a code naming the status ("http_404"), or "provider_error" for a
// failure reported in a 2xx (Slack's ok:false) or with no status at all.
func IntegrationAnswerOutcome(isError bool, status int) (outcome, errorCode string) {
	if !isError {
		return IntegrationOutcomeOK, ""
	}
	if status != 0 && (status < 200 || status >= 300) {
		return IntegrationOutcomeError, "http_" + strconv.Itoa(status)
	}
	return IntegrationOutcomeError, "provider_error"
}
