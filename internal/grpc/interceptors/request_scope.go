// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

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
func RequestScope(traceIC connect.ServerInterceptor) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return traceIC(func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			ctx = telemetry.WithRequestScope(ctx)
			stamper := newRunStamper(trace.SpanFromContext(ctx))
			if spec.StreamType != connect.StreamTypeUnary {
				return next(ctx, spec, &runStampingStream{ServerStream: stream, stamper: stamper})
			}
			// A unary request's ids scope the handler's logs, so the
			// request is read here, ahead of the handler, and replayed to
			// it; connect v1 had read it before any interceptor ran.
			req := newRequest(spec)
			if req == nil {
				return next(ctx, spec, &responseStampingStream{ServerStream: stream, stamper: stamper})
			}
			if err := stream.Receive(req); err != nil {
				return err
			}
			if ids := stamper.stamp(req); len(ids) > 0 {
				ctx = telemetry.WithRunScope(ctx, runScope(ids))
			}
			return next(ctx, spec, &responseStampingStream{ServerStream: &replayStream{ServerStream: stream, req: req}, stamper: stamper})
		})
	}
}

// runStampingStream stamps the stream's span from the first message it
// receives that names a run.
type runStampingStream struct {
	connect.ServerStream
	stamper *runStamper
	done    bool
}

func (c *runStampingStream) Receive(msg any) error {
	err := c.ServerStream.Receive(msg)
	if err == nil && !c.done && len(c.stamper.stamp(msg)) > 0 {
		c.done = true
	}
	return err
}

// responseStampingStream stamps a unary call's span from its response, for
// an id the request did not carry (StartChat returns the chat it made).
type responseStampingStream struct {
	connect.ServerStream
	stamper *runStamper
}

func (c *responseStampingStream) Send(msg any) error {
	c.stamper.stamp(msg)
	return c.ServerStream.Send(msg)
}

// newRequest is an empty request message of spec's procedure, or nil when the
// spec does not say (a hand-built spec with no protobuf schema).
func newRequest(spec connect.Spec) proto.Message {
	md, ok := spec.Schema.(protoreflect.MethodDescriptor)
	if !ok {
		return nil
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(md.Input().FullName())
	if err != nil {
		return nil
	}
	return mt.New().Interface()
}

// replayStream hands the handler the request an interceptor already read.
type replayStream struct {
	connect.ServerStream
	req      proto.Message
	received bool
}

func (s *replayStream) Receive(msg any) error {
	if s.received {
		return s.ServerStream.Receive(msg)
	}
	dst, ok := msg.(proto.Message)
	if !ok {
		return fmt.Errorf("interceptors: cannot receive into %T", msg)
	}
	s.received = true
	proto.Reset(dst)
	proto.Merge(dst, s.req)
	return nil
}
