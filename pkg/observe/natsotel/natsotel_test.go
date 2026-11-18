package natsotel

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestPublishAndConsumeShareOneTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	ctx, publisher := tp.Tracer("t").Start(context.Background(), "rpc")
	msg := NewMsg(ctx, "orders.created", []byte("{}"))
	publisher.End()
	if msg.Header.Get("traceparent") == "" {
		t.Fatal("NewMsg injected no traceparent")
	}

	_, consumer := StartConsumerSpan(context.Background(), msg.Subject, msg.Header, "process orders")
	consumer.End()
	got := consumer.(sdktrace.ReadOnlySpan)
	if got.Parent().SpanID() != publisher.SpanContext().SpanID() {
		t.Error("consumer span is not a child of the publisher's span")
	}
	if got.SpanKind() != trace.SpanKindConsumer {
		t.Errorf("kind = %v", got.SpanKind())
	}

	// No headers: a root span, not a panic.
	_, root := StartConsumerSpan(context.Background(), "x", nil, "process")
	root.End()
	if root.(sdktrace.ReadOnlySpan).Parent().IsValid() {
		t.Error("span with no trace context has a parent")
	}

	// Inject on a message without headers creates them.
	m := &nats.Msg{Subject: "y"}
	Inject(ctx, m)
	if m.Header.Get("traceparent") == "" {
		t.Error("Inject did not create headers")
	}
}
