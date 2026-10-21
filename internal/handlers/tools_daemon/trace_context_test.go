package tools_daemon

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// The gateway hands the daemon the trace of the request it is forwarding, as
// W3C headers on the ToolRequest; with no span in ctx it sends none.
func TestTraceContextOfCarriesTheCallersTrace(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "nats.handle.tools.request")
	defer span.End()

	tc := traceContextOf(ctx)
	if want := span.SpanContext().TraceID().String(); !strings.Contains(tc["traceparent"], want) {
		t.Fatalf("traceparent %q does not carry the caller's trace %s", tc["traceparent"], want)
	}
	if got := traceContextOf(context.Background()); got != nil {
		t.Errorf("no span in ctx must send no trace context, got %v", got)
	}
}
