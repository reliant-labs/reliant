// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

type passthrough struct{}

func (passthrough) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }
func (passthrough) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (passthrough) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// The layer that logs a failed RPC sits outside authentication; the user the
// authenticator binds must still reach it, and the server span.
func TestRequestScopeLetsAuthAttributeOuterLayers(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := tp.Tracer("t").Start(context.Background(), "rpc")

	var outer context.Context
	handler := RequestScope(passthrough{}).WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		outer = ctx
		telemetry.BindUser(ctx, "user-1")
		return nil, nil
	})
	_, err := handler(ctx, nil)
	require.NoError(t, err)
	span.End()

	assert.Equal(t, "user-1", telemetry.UserIDFromContext(outer))
	require.True(t, trace.SpanFromContext(ctx).SpanContext().IsValid())
	ended := recorder.Ended()
	require.Len(t, ended, 1)
	assert.Contains(t, ended[0].Attributes(), attribute.String(telemetry.AttrUserID, "user-1"))
}
