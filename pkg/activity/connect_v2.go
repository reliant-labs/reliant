package activity

import (
	"context"
	"errors"

	connectv2 "connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// DefaultViaV2 is [DefaultVia] for connect-go v2: the call's Forge-Via
// request header, lowercased and limited to a short token, or "api" when
// absent or malformed.
func DefaultViaV2(_ context.Context, info *connectv2.CallInfo) string {
	return viaFromValue(info.RequestHeader().Get(ViaHeader))
}

// InterceptorV2 is [Interceptor] for connect-go v2. It takes the same Deps,
// refuses the same ones (a nil Principal), resolves the same declarations
// from the method descriptor in Spec.Schema, and writes the same events:
// on a unary call's success, the declared fields read from the request the
// handler received and the response it sent; nothing for a failed call; for
// a streaming rpc, only the call record a HANDLER-written stream's
// [Record] reads. The channel comes from Deps.ViaFromV2 ([DefaultViaV2]
// when nil) for a unary call, and from the Forge-Via header for a stream, as
// in v1. It refuses Deps that set ViaFrom without ViaFromV2.
//
// A v2 handler sends its unary response on the stream instead of returning
// it, so the interceptor holds the response until the event is written and
// then sends it. The ordering is the v1 interceptor's: the event is written
// before the caller receives the response.
func InterceptorV2(deps Deps) (connectv2.ServerInterceptor, error) {
	if deps.ViaFromV2 == nil && deps.ViaFrom != nil {
		return nil, errors.New("activity.InterceptorV2: Deps.ViaFrom is set but Deps.ViaFromV2 is not — " +
			"this is the connect-go v2 interceptor, which reads ViaFromV2 (ViaFrom takes a v1 request); set ViaFromV2 too")
	}
	ic, err := newInterceptor(deps, "activity.InterceptorV2")
	if err != nil {
		return nil, err
	}
	return ic.wrapV2, nil
}

func (i *interceptor) wrapV2(next connectv2.ServerFunc) connectv2.ServerFunc {
	return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
		decl := i.activeFor(spec.Procedure, spec.Schema)
		if decl == nil {
			return next(ctx, spec, stream)
		}
		info, ok := connectv2.CallInfoForServerContext(ctx)
		if !ok {
			info = &connectv2.CallInfo{}
		}
		if spec.StreamType != connectv2.StreamTypeUnary {
			// As WrapStreamingHandler: install the call record a
			// HANDLER-written stream's Record reads, and write nothing.
			c := &call{
				decl:      decl,
				actor:     i.deps.Principal.Actor(ctx),
				via:       viaFromValue(info.RequestHeader().Get(ViaHeader)),
				scope:     i.deps.Principal.Scope(ctx),
				procedure: spec.Procedure,
			}
			err := next(context.WithValue(ctx, callKey{}, c), spec, stream)
			if err == nil && !c.recorded.Load() {
				i.deps.Logger.WarnContext(ctx, "activity: streaming rpc succeeded without calling activity.Record; no event was written",
					"procedure", c.procedure)
			}
			return err
		}

		c := &call{
			decl:      decl,
			actor:     i.deps.Principal.Actor(ctx),
			via:       i.deps.ViaFromV2(ctx, info),
			scope:     i.deps.Principal.Scope(ctx),
			procedure: spec.Procedure,
		}
		if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
			c.traceID = sc.TraceID().String()
		}
		held := &heldResponseStream{ServerStream: stream, call: c}
		err := next(context.WithValue(ctx, callKey{}, c), spec, held)
		if err != nil {
			_ = held.flush() // the handler failed; what it sent, if anything, still goes out
			return err
		}
		if c.recorded.Load() {
			return held.flush()
		}
		if decl.Writer == WriterHandler {
			i.deps.Logger.WarnContext(ctx, "activity: rpc declares writer HANDLER but its handler did not call activity.Record; the interceptor wrote the event",
				"procedure", c.procedure)
		}
		var respMsg protoreflect.Message
		if m, ok := held.msg.(proto.Message); ok && m != nil {
			respMsg = m.ProtoReflect()
		}
		ev := c.event(respMsg)
		if ev.Target.ID == "" {
			i.deps.Logger.WarnContext(ctx, "activity: declared target id is empty in this call; the event is recorded without one",
				"procedure", c.procedure, "path", decl.TargetID.Spec)
		}
		i.write(ctx, ev)
		return held.flush()
	}
}

// heldResponseStream is a unary call's stream as the activity interceptor
// passes it to the handler: Receive records the request on the call (the
// paths Record and the event read), and Send holds the response until
// flush, so the event is written before the caller sees the response.
type heldResponseStream struct {
	connectv2.ServerStream
	call *call
	msg  any
	held bool
}

func (s *heldResponseStream) Receive(msg any) error {
	err := s.ServerStream.Receive(msg)
	if err == nil && s.call.req == nil {
		if m, ok := msg.(proto.Message); ok && m != nil {
			s.call.req = m.ProtoReflect()
		}
	}
	return err
}

func (s *heldResponseStream) Send(msg any) error {
	if err := s.flush(); err != nil { // a second Send releases the first in order
		return err
	}
	s.msg, s.held = msg, true
	return nil
}

// flush sends the held response, if any.
func (s *heldResponseStream) flush() error {
	if !s.held {
		return nil
	}
	s.held = false
	return s.ServerStream.Send(s.msg)
}
