package daemonruntime

import (
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/observability"
)

// A tool call executes under the trace that dispatched it: the daemon
// continues the W3C context the gateway put on the ToolRequest, so its log
// lines for the execution carry the dispatching trace's trace_id. With no
// context on the request, the execution has no trace.
func TestDispatchContextContinuesTheGatewaysTrace(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	observability.InstallPropagation()
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	const traceparent = "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01"
	ctx := dispatchContext(&reliantv1.ToolRequest{TraceContext: map[string]string{"traceparent": traceparent}})
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() || sc.TraceID().String() != "0102030405060708090a0b0c0d0e0f10" || !sc.IsRemote() {
		t.Fatalf("execution context did not continue the dispatching trace: %+v", sc)
	}

	if trace.SpanContextFromContext(dispatchContext(&reliantv1.ToolRequest{})).IsValid() {
		t.Error("a request with no trace context must not gain one")
	}
}

// Without InstallPropagation the global propagator is a no-op and the trace
// context on the request is silently dropped — which is why the daemon
// installs it at start.
func TestDispatchContextNeedsThePropagator(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	ctx := dispatchContext(&reliantv1.ToolRequest{TraceContext: map[string]string{"traceparent": "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01"}})
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Error("expected the no-op propagator to drop the context")
	}
}

// A daemon command runs under the gateway's `gateway.daemon.command` span the
// same way, from DaemonCommandRequest.trace_context.
func TestCommandDispatchContextContinuesTheGatewaysTrace(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	observability.InstallPropagation()
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	const traceparent = "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01"
	ctx := commandDispatchContext(&reliantv1.DaemonCommandRequest{TraceContext: map[string]string{"traceparent": traceparent}})
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() || sc.TraceID().String() != "0102030405060708090a0b0c0d0e0f10" || sc.SpanID().String() != "0102030405060708" {
		t.Fatalf("command context did not continue the gateway span: %+v", sc)
	}
	if trace.SpanContextFromContext(commandDispatchContext(&reliantv1.DaemonCommandRequest{})).IsValid() {
		t.Error("a command with no trace context must not gain one")
	}
}
