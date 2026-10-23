// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// RequestScope wraps the chain's trace interceptor so every layer inside the
// server span runs with a telemetry request scope. Authentication runs deep
// inside the chain, after logging and tracing have started; the scope is how
// it tells them whose request they are recording (telemetry.BindUser), so the
// "rpc failed" record and both server spans carry user.id.
func RequestScope(trace connect.Interceptor) connect.Interceptor {
	return requestScope{trace: trace}
}

type requestScope struct{ trace connect.Interceptor }

func (s requestScope) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return s.trace.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(telemetry.WithRequestScope(ctx), req)
	})
}

func (s requestScope) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return s.trace.WrapStreamingClient(next)
}

func (s requestScope) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return s.trace.WrapStreamingHandler(func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(telemetry.WithRequestScope(ctx), conn)
	})
}
