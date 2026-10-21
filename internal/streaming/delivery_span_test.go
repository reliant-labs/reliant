package streaming

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe/natsotel"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// The API server opens one span per producer stream a subscriber receives,
// not one per delta: per-delta spans were 94% of its spans in prod.
func TestDeliverySpanIsOnePerProducerStream(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	producer := tp.Tracer("producer")
	headersOf := func(span trace.Span) nats.Header {
		msg := &nats.Msg{}
		natsotel.Inject(trace.ContextWithSpan(context.Background(), span), msg)
		return msg.Header
	}
	text := StreamingDelta{DeltaType: DeltaTypeContentBlockDelta, Delta: "x"}
	stop := StreamingDelta{DeltaType: DeltaTypeMessageStop}

	_, a := producer.Start(context.Background(), "call_llm.a")
	_, b := producer.Start(context.Background(), "call_llm.b")
	_, c := producer.Start(context.Background(), "call_llm.c")

	d := &deliverySpan{chatID: "chat-1"}
	ctx := context.Background()
	for range 4 {
		d.observe(ctx, headersOf(a), text)
	}
	d.observe(ctx, headersOf(a), stop) // ends a's stream: 5 deltas
	for range 3 {
		d.observe(ctx, headersOf(b), text) // b never sends message_stop
	}
	d.observe(ctx, nats.Header{}, text) // no trace context: no span, ends b
	for range 2 {
		d.observe(ctx, headersOf(c), text)
	}
	d.end() // subscription ends: c's stream, 2 deltas

	ended := rec.Ended()
	if len(ended) != 3 {
		t.Fatalf("got %d spans, want 3 (one per producer stream)", len(ended))
	}
	for i, want := range []struct {
		parent trace.Span
		deltas int64
	}{{a, 5}, {b, 3}, {c, 2}} {
		s := ended[i]
		if s.Name() != "nats.consume.streaming" {
			t.Errorf("span %d name = %q", i, s.Name())
		}
		if s.Parent().SpanID() != want.parent.SpanContext().SpanID() {
			t.Errorf("span %d is not a child of its producer span", i)
		}
		var deltas int64 = -1
		for _, kv := range s.Attributes() {
			if kv.Key == "streaming.deltas" {
				deltas = kv.Value.AsInt64()
			}
		}
		if deltas != want.deltas {
			t.Errorf("span %d streaming.deltas = %d, want %d", i, deltas, want.deltas)
		}
	}
}
