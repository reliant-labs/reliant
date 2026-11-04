// Copyright (c) 2025 Reliant Labs
package streaming

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/observe"
)

// A StreamChatUpdates server span lasts as long as the client keeps the chat
// open — minutes, typically — so its duration says nothing about how long the
// chat took to load. These child spans do: one per phase of the initial sync,
// each named for what it reads, plus the chat_stream.caught_up event on the
// RPC span that marks the moment the client had everything.

// startSpan starts a child span of ctx's span. The tracer is resolved per
// call, not cached: a package-level tracer taken before the process installs
// its provider would stay a no-op.
func startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer("reliant.streaming").Start(ctx, name, trace.WithAttributes(attrs...))
}

// traced runs fn inside a span named name, recording its error on the span.
func traced(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	ctx, span := startSpan(ctx, name)
	defer span.End()
	err := fn(ctx)
	observe.RecordSpanError(span, err)
	return err
}
