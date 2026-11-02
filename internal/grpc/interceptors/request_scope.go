// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// RequestScope wraps the chain's trace interceptor so every layer inside the
// server span runs with a telemetry request scope. Authentication runs deep
// inside the chain, after logging and tracing have started; the scope is how
// it tells them whose request they are recording (telemetry.BindUser), so the
// "rpc failed" record and both server spans carry user.id.
//
// It also names the run the RPC is about on the server span: chat.id,
// workflow.id and thread.id from the request's top-level fields of those
// names (runAttrs), then from the response's for an id the request did not
// carry (StartChat returns the chat it made). That is what lets a trace be
// found by its chat. A unary request's ids also scope the logs written
// inside it (telemetry.WithRunScope). A stream is stamped from its first
// message.
func RequestScope(trace connect.Interceptor) connect.Interceptor {
	return requestScope{trace: trace}
}

type requestScope struct{ trace connect.Interceptor }

func (s requestScope) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return s.trace.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx = telemetry.WithRequestScope(ctx)
		stamper := newRunStamper(trace.SpanFromContext(ctx))
		if req != nil {
			if ids := stamper.stamp(req.Any()); len(ids) > 0 {
				ctx = telemetry.WithRunScope(ctx, runScope(ids))
			}
		}
		resp, err := next(ctx, req)
		if err == nil && resp != nil {
			stamper.stamp(resp.Any())
		}
		return resp, err
	})
}

func (s requestScope) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return s.trace.WrapStreamingClient(next)
}

func (s requestScope) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return s.trace.WrapStreamingHandler(func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx = telemetry.WithRequestScope(ctx)
		return next(ctx, &runStampingConn{StreamingHandlerConn: conn, stamper: newRunStamper(trace.SpanFromContext(ctx))})
	})
}

// runStampingConn stamps the stream's span from the first message it receives
// that names a run.
type runStampingConn struct {
	connect.StreamingHandlerConn
	stamper *runStamper
	done    bool
}

func (c *runStampingConn) Receive(msg any) error {
	err := c.StreamingHandlerConn.Receive(msg)
	if err == nil && !c.done && len(c.stamper.stamp(msg)) > 0 {
		c.done = true
	}
	return err
}
