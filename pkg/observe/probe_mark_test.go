package observe

import (
	"context"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// The server span and the innermost span both carry forge.probe=true, stored
// as the "true" SpanNotProbeSQL compares against.
func TestMarkProbeSpansMarksServerAndInnerSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	tracer := tp.Tracer("t")
	ctx, server := tracer.Start(context.Background(), "grpc.health.v1.Health/Check")
	ctx = context.WithValue(ctx, serverSpanKey{}, server)
	ctx, inner := tracer.Start(ctx, "inner")
	MarkProbeSpans(ctx)
	inner.End()
	server.End()
	for _, s := range rec.Ended() {
		var got attribute.Value
		for _, kv := range s.Attributes() {
			if string(kv.Key) == attrForgeProbe {
				got = kv.Value
			}
		}
		if got.Emit() != "true" {
			t.Errorf("span %s: forge.probe = %q, want \"true\"", s.Name(), got.Emit())
		}
	}
	if len(rec.Ended()) != 2 {
		t.Fatalf("recorded %d spans, want 2", len(rec.Ended()))
	}
}

// With no tracer provider recording, the request's spans are otel's
// non-recording spans, whose dynamic type is not comparable. Marking them is a
// no-op, never a panic (a dev stack with no collector served every probe
// health check as a recovered panic).
func TestMarkProbeSpansWithNonRecordingSpans(t *testing.T) {
	ctx := trace.ContextWithSpanContext(context.Background(), trace.SpanContext{})
	// No server span recorded: serverSpanOf falls back to the innermost span.
	MarkProbeSpans(ctx)
	// A server span recorded, and a second non-recording span inside it.
	ctx = context.WithValue(ctx, serverSpanKey{}, trace.SpanFromContext(ctx))
	MarkProbeSpans(trace.ContextWithSpanContext(ctx, trace.SpanContext{}))
}

// Only the health check that carries the probe header is a probe: a monitor's
// health check, and any other procedure with the header, are real traffic.
func TestIsHealthProbe(t *testing.T) {
	probe := http.Header{HeaderHealthProbe: {"env-up"}}
	for _, tc := range []struct {
		procedure string
		h         http.Header
		want      bool
	}{
		{HealthCheckProcedure, probe, true},
		{HealthCheckProcedure, http.Header{}, false},
		{"/grpc.health.v1.Health/Watch", probe, false},
		{"/acme.v1.Shop/List", probe, false},
	} {
		if got := IsHealthProbe(tc.procedure, tc.h); got != tc.want {
			t.Errorf("IsHealthProbe(%s, %v) = %v, want %v", tc.procedure, tc.h, got, tc.want)
		}
	}
}
