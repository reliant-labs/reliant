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
	"google.golang.org/protobuf/proto"
)

// Interceptor returns a Connect interceptor that validates every request
// message against its protovalidate rules. It builds one shared
// protovalidate.Validator (which compiles each message's rules once and
// caches them), so the interceptor is cheap per-call. Returns an error
// only if the validator itself cannot be constructed.
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
