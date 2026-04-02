// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var natsTracer = otel.Tracer("reliant.nats")

// natsHeaderCarrier adapts nats.Header to the OTel TextMapCarrier interface.
type natsHeaderCarrier nats.Header

func (c natsHeaderCarrier) Get(key string) string {
	return nats.Header(c).Get(key)
}

func (c natsHeaderCarrier) Set(key, value string) {
	nats.Header(c).Set(key, value)
}

func (c natsHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// InjectTraceContext injects the current span context into NATS message headers.
// If the message has no headers, they will be initialized.
func InjectTraceContext(ctx context.Context, msg *nats.Msg) {
	if msg.Header == nil {
		msg.Header = make(nats.Header)
	}
	otel.GetTextMapPropagator().Inject(ctx, natsHeaderCarrier(msg.Header))
}

// ExtractTraceContext extracts a span context from NATS message headers
// and returns a new context with the extracted span as parent.
func ExtractTraceContext(ctx context.Context, msg *nats.Msg) context.Context {
	if msg.Header == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, natsHeaderCarrier(msg.Header))
}

// StartNATSSpan extracts trace context from NATS headers and starts a new span.
// Returns the context with the span and the span itself. Caller must call span.End().
func StartNATSSpan(ctx context.Context, msg *nats.Msg, operationName string) (context.Context, trace.Span) {
	ctx = ExtractTraceContext(ctx, msg)
	return natsTracer.Start(ctx, operationName)
}

// NATSPublishMsg creates a NATS message with trace context injected.
func NATSPublishMsg(ctx context.Context, subject string, data []byte) *nats.Msg {
	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header:  make(nats.Header),
	}
	InjectTraceContext(ctx, msg)
	return msg
}

// PropagatorFromContext returns the global propagator. Useful for NATS middleware.
func PropagatorFromContext() propagation.TextMapPropagator {
	return otel.GetTextMapPropagator()
}
