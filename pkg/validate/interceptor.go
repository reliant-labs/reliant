// Package validate provides a Connect interceptor that enforces
// protovalidate (buf.validate) field rules at the RPC boundary.
//
// It is the WIRE arm of forge's "one declaration, three enforcement
// points" contract: a rule declared once on a proto field —
// `int64 amount_cents = 1 [(buf.validate.field).int64.gte = 0];` — is
// enforced here on every request BEFORE the handler runs (invalid
// requests are rejected with CodeInvalidArgument), while the same rule
// also projects to a DB CHECK at entity birth and a zod validator in the
// generated form.
//
// The interceptor is generic: it validates whatever protovalidate rules a
// request message carries, so it covers the full protovalidate surface
// (CEL, string formats, repeated/map rules, ...) — not just the subset
// forge projects to the DB/form layers. Wiring is a single line in the
// generated serve composition root; the machinery lives here.
package validate

import (
	"context"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
)

// Interceptor returns a Connect interceptor that validates every request
// message against its protovalidate rules. It builds one shared
// protovalidate.Validator (which compiles each message's rules once and
// caches them), so the interceptor is cheap per-call. Returns an error
// only if the validator itself cannot be constructed.
//
// connect-go v2 projects use [InterceptorV2].
func Interceptor() (connect.Interceptor, error) {
	v, err := protovalidate.New()
	if err != nil {
		return nil, err
	}
	return &interceptor{v: v}, nil
}

type interceptor struct {
	v protovalidate.Validator
}

func (i *interceptor) validate(msg any) error {
	m, ok := msg.(proto.Message)
	if !ok {
		return nil
	}
	if err := i.v.Validate(m); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

// WrapUnary validates the request before the handler sees it.
func (i *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := i.validate(req.Any()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient is a no-op: request validation is a server concern
// (an interceptor mounted on handlers). Clients pass through untouched.
func (i *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler validates each message the handler receives off a
// client/bidi stream.
func (i *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(ctx, &validatingConn{StreamingHandlerConn: conn, i: i})
	}
}

// validatingConn wraps a streaming handler connection so every received
// message is validated as it arrives.
type validatingConn struct {
	connect.StreamingHandlerConn
	i *interceptor
}

func (c *validatingConn) Receive(msg any) error {
	if err := c.StreamingHandlerConn.Receive(msg); err != nil {
		return err
	}
	return c.i.validate(msg)
}

// InterceptorV2 is [Interceptor] for connect-go v2: a server interceptor
// that validates every request message against its protovalidate rules and
// fails the call with CodeInvalidArgument, carrying the violation text as the
// message — the same code and message v1's interceptor sends. Returns an
// error only if the validator itself cannot be constructed.
//
// v2 has one interceptor shape for every stream type, and a handler reads its
// request from the stream, so validation happens as each message is
// received: for a unary or server-streaming rpc that is the handler's single
// Receive, which fails before the handler method runs, exactly as v1's
// WrapUnary refused before calling next; for a client or bidi stream it is
// each Receive, as v1's WrapStreamingHandler did. Interceptors outside this
// one see the same error either way.
func InterceptorV2() (connectv2.ServerInterceptor, error) {
	v, err := protovalidate.New()
	if err != nil {
		return nil, err
	}
	return (&interceptor{v: v}).serverV2, nil
}

func (i *interceptor) serverV2(next connectv2.ServerFunc) connectv2.ServerFunc {
	return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
		return next(ctx, spec, &validatingStreamV2{ServerStream: stream, i: i})
	}
}

// validateV2 is validate with the v2 error type: the same code, and the
// violation text as the message (v1's connect.NewError(code, err) publishes
// err.Error()).
func (i *interceptor) validateV2(msg any) error {
	m, ok := msg.(proto.Message)
	if !ok {
		return nil
	}
	if err := i.v.Validate(m); err != nil {
		return connectv2.NewError(connectv2.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	return nil
}

// validatingStreamV2 validates every request message as the handler
// receives it.
type validatingStreamV2 struct {
	connectv2.ServerStream
	i *interceptor
}

func (s *validatingStreamV2) Receive(msg any) error {
	if err := s.ServerStream.Receive(msg); err != nil {
		return err
	}
	return s.i.validateV2(msg)
}
