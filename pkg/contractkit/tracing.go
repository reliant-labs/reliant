package contractkit

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// TraceStart starts a span on tracer with the given operation name.
// If ctx is nil, context.Background() is used (matching the previous
// generated code's fallback for context-less methods).
//
// The returned context carries the span and should be propagated to
// the inner method call (or held as the call's stand-in context for
// methods without a context parameter).
//
// Callers are responsible for calling span.End(); the canonical
// pattern is:
//
//	ctx, span := contractkit.TraceStart(ctx, tracer, "Service.Send")
//	defer span.End()
//	err := inner.Send(ctx, ...)
//	contractkit.RecordSpanError(span, err)
//	return err
func TraceStart(ctx context.Context, tracer trace.Tracer, name string) (context.Context, trace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	return tracer.Start(ctx, name)
}

// RecordSpanError records err on span if err is non-nil and sets the
// span status to codes.Error with err.Error() as the description.
//
// This is identical in behaviour to the previous generated code's
// per-method block:
//
//	if err != nil {
//	    span.RecordError(err)
//	    span.SetStatus(codes.Error, err.Error())
//	}
//
// nil-safe on err. Does nothing for a nil span.
func RecordSpanError(span trace.Span, err error) {
	if err == nil || span == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
