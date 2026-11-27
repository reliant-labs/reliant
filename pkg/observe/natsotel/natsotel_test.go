package natsotel

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
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

func useRecorder(t *testing.T) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return tp, rec
}

func attrMap(span sdktrace.ReadOnlySpan) map[string]string {
	m := map[string]string{}
	for _, kv := range span.Attributes() {
		m[string(kv.Key)] = kv.Value.AsString()
	}
	return m
}

func TestHeaderCarrierKeysListsEveryHeader(t *testing.T) {
	c := headerCarrier(nats.Header{"Traceparent": {"x"}, "Baggage": {"k=v"}})
	got := c.Keys()
	sort.Strings(got)
	if want := []string{"Baggage", "Traceparent"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Keys() = %v, want %v", got, want)
	}
	if keys := headerCarrier(nats.Header{}).Keys(); len(keys) != 0 {
		t.Errorf("Keys() of empty header = %v, want none", keys)
	}
}

func TestExtractWithNilHeaderReturnsContextUnchanged(t *testing.T) {
	useRecorder(t)
	ctx := context.WithValue(context.Background(), struct{}{}, "marker")
	if got := Extract(ctx, nil); got != ctx {
		t.Error("Extract(ctx, nil) must return ctx itself")
	}
}

func TestStartProducerSpanIsAProducerParentOfTheConsumer(t *testing.T) {
	tp, _ := useRecorder(t)
	ctx, parent := tp.Tracer("t").Start(context.Background(), "request")
	defer parent.End()

	pctx, producer := StartProducerSpan(ctx, "orders.created", "publish orders")
	msg := NewMsg(pctx, "orders.created", nil)
	producer.End()

	ro := producer.(sdktrace.ReadOnlySpan)
	if ro.Name() != "publish orders" {
		t.Errorf("name = %q", ro.Name())
	}
	if ro.SpanKind() != trace.SpanKindProducer {
		t.Errorf("kind = %v, want producer", ro.SpanKind())
	}
	if ro.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Error("producer span is not a child of the caller's span")
	}
	want := map[string]string{
		"messaging.system":           "nats",
		"messaging.destination.name": "orders.created",
		"messaging.operation.type":   "send",
	}
	if got := attrMap(ro); !reflect.DeepEqual(got, want) {
		t.Errorf("attributes = %v, want %v", got, want)
	}

	_, consumer := StartConsumerSpan(context.Background(), msg.Subject, msg.Header, "process")
	consumer.End()
	cro := consumer.(sdktrace.ReadOnlySpan)
	if cro.Parent().SpanID() != producer.SpanContext().SpanID() {
		t.Error("consumer is not a child of the producer span")
	}
	if cro.SpanContext().TraceID() != parent.SpanContext().TraceID() {
		t.Error("producer and consumer are on different traces")
	}
	if got := attrMap(cro)["messaging.operation.type"]; got != "process" {
		t.Errorf("consumer operation type = %q, want process", got)
	}
}

func TestRecordError(t *testing.T) {
	tp, _ := useRecorder(t)

	t.Run("marks the span failed", func(t *testing.T) {
		_, span := tp.Tracer("t").Start(context.Background(), "failing")
		RecordError(span, errors.New("handler exploded"))
		span.End()

		ro := span.(sdktrace.ReadOnlySpan)
		if ro.Status().Code != codes.Error || ro.Status().Description != "handler exploded" {
			t.Errorf("status = %+v", ro.Status())
		}
		events := ro.Events()
		if len(events) != 1 || events[0].Name != "exception" {
			t.Errorf("events = %+v, want one exception event", events)
		}
	})

	t.Run("nil error leaves the span untouched", func(t *testing.T) {
		_, span := tp.Tracer("t").Start(context.Background(), "ok")
		RecordError(span, nil)
		span.End()

		ro := span.(sdktrace.ReadOnlySpan)
		if ro.Status().Code != codes.Unset {
			t.Errorf("status = %+v, want unset", ro.Status())
		}
		if len(ro.Events()) != 0 {
			t.Errorf("events = %+v, want none", ro.Events())
		}
	})
}
