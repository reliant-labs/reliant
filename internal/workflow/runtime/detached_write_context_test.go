// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// A best-effort write after an activity must survive the activity's
// cancellation AND stay in its trace. context.Background() did the first and
// broke the second: every step_executions INSERT was a parentless
// `orm.CreateStepExecution` root.
func TestDetachedWriteContextKeepsTheTraceAndDropsTheCancel(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	activityCtx, cancelActivity := context.WithCancel(context.Background())
	activityCtx, span := tp.Tracer("test").Start(activityCtx, "RunActivity:CallLLM")
	defer span.End()
	cancelActivity()

	writeCtx, cancel := detachedWriteContext(activityCtx)
	defer cancel()

	if err := writeCtx.Err(); err != nil {
		t.Fatalf("write context inherited the activity's cancellation: %v", err)
	}
	if _, ok := writeCtx.Deadline(); !ok {
		t.Fatal("write context has no deadline of its own")
	}
	if got, want := trace.SpanContextFromContext(writeCtx).SpanID(), span.SpanContext().SpanID(); got != want {
		t.Fatalf("write context lost the activity span: got %s, want %s", got, want)
	}
}
