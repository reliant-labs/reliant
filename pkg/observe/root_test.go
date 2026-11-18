package observe

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestRunRootStartsANewTraceAndNestsTheWork(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	// Started inside a request: the iteration must not join the request's trace.
	reqCtx, req := tp.Tracer("t").Start(context.Background(), "rpc")
	var child trace.SpanContext
	err := RunRoot(reqCtx, "worker.sync.tick", func(ctx context.Context) error {
		_, s := tp.Tracer("t").Start(ctx, "db")
		child = s.SpanContext()
		s.End()
		return errors.New("boom")
	})
	req.End()
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
	var root sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "worker.sync.tick" {
			root = s
		}
	}
	if root == nil {
		t.Fatal("no root span")
	}
	if root.Parent().IsValid() || root.SpanContext().TraceID() == req.SpanContext().TraceID() {
		t.Error("RunRoot joined the caller's trace")
	}
	if child.TraceID() != root.SpanContext().TraceID() {
		t.Error("work inside fn is not in the root's trace")
	}
	if root.Status().Code != codes.Error {
		t.Error("error not recorded")
	}
}

func TestRunRootRecordsAndReraisesPanics(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	defer func() {
		if recover() == nil {
			t.Fatal("panic swallowed")
		}
		if ended := rec.Ended(); len(ended) != 1 || ended[0].Status().Code != codes.Error {
			t.Fatalf("panicking iteration's span not ended as an error: %v", ended)
		}
	}()
	_ = RunRoot(context.Background(), "x", func(context.Context) error { panic("bad") })
}
