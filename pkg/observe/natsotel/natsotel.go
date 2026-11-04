// Package natsotel carries trace context across NATS, so a message's consumer
// continues the trace of the code that published it.
//
// NATS has no instrumentation of its own: a message is bytes and headers, and
// a handler that does not read the traceparent out of those headers starts a
// new, unrelated trace. These helpers are the two halves — inject on publish,
// extract on consume — using the global OpenTelemetry propagator (W3C
// trace-context + baggage, installed by observe.Setup), so NATS carries the
// same context Connect, HTTP and Temporal do.
//
// Publish:
//
//	nc.PublishMsg(natsotel.NewMsg(ctx, subject, data))
//
// Consume (core NATS or JetStream — pass the subject and headers):
//
//	ctx, span := natsotel.StartConsumerSpan(ctx, msg.Subject, msg.Header, "process orders")
//	defer span.End()
//
// Lifted from Reliant, where NATS propagation is what made API → worker →
// daemon traces work.
package natsotel

import (
	"context"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// TracerName is the instrumentation scope of the spans this package creates.
const TracerName = "github.com/reliant-labs/forge/pkg/observe/natsotel"

// headerCarrier adapts nats.Header to propagation.TextMapCarrier. NATS
// headers are case-sensitive maps; Get/Set use nats.Header's own accessors.
type headerCarrier nats.Header

func (c headerCarrier) Get(key string) string { return nats.Header(c).Get(key) }
func (c headerCarrier) Set(key, value string) { nats.Header(c).Set(key, value) }
func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// Inject writes ctx's span context into msg's headers, creating them if
// absent.
func Inject(ctx context.Context, msg *nats.Msg) {
	if msg.Header == nil {
		msg.Header = nats.Header{}
	}
	otel.GetTextMapPropagator().Inject(ctx, headerCarrier(msg.Header))
}

// Extract returns ctx carrying the span context found in header, if any.
func Extract(ctx context.Context, header nats.Header) context.Context {
	if header == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, headerCarrier(header))
}

// NewMsg builds a message for subject with ctx's trace context in its headers.
func NewMsg(ctx context.Context, subject string, data []byte) *nats.Msg {
	msg := &nats.Msg{Subject: subject, Data: data, Header: nats.Header{}}
	Inject(ctx, msg)
	return msg
}

// StartConsumerSpan starts a CONSUMER span named name for a message received
// on subject, as a child of the publisher's span carried in header. With no
// trace context in header it is a root span. The caller ends it.
func StartConsumerSpan(ctx context.Context, subject string, header nats.Header, name string) (context.Context, trace.Span) {
	ctx = Extract(ctx, header)
	return otel.Tracer(TracerName).Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", subject),
			attribute.String("messaging.operation.type", "process"),
		))
}

// StartProducerSpan starts a PRODUCER span for publishing to subject. Inject
// the returned ctx (NewMsg does) so consumers become its children. Use it
// where the publish itself is worth a span — a request/reply, a fan-out — not
// for every fire-and-forget event.
func StartProducerSpan(ctx context.Context, subject, name string) (context.Context, trace.Span) {
	return otel.Tracer(TracerName).Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", subject),
			attribute.String("messaging.operation.type", "send"),
		))
}

// RecordError marks span failed with err; a nil err is a no-op.
func RecordError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
