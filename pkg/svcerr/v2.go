package svcerr

// v2.go — the connect-go v2 surface.
//
// svcerr serves projects on either connect major. The constructors, Wrap,
// WrapOr, WithCause, Classify and the Is* predicates are connect-free and
// shared; what differs is the wire type at the boundary, so each exported
// symbol that names a v1 type has a V2 twin here: [InterceptorV2],
// [ToConnectV2], [CodeV2] and [NewV2]. [RejectV2] has no twin: it is what a
// v2 interceptor returns to reject a call, where a v1 one returned the
// *connect.Error itself.
//
// # One verdict, two encodings
//
// The v2 surface does not re-derive anything. Every v2 error is computed by
// [ToConnect] — the v1 verdict, with its code, message, details and error
// metadata exactly as a v1 server would put them on the wire — and then
// re-encoded as a *connectv2.Error. That is what makes the wire identical
// between the majors by construction rather than by a second implementation
// kept in step with the first.
//
// Classification recognises both majors everywhere: a *connect.Error of
// either major on an error's client view is a verdict ([IsClassified]), its
// code is the code ([Code], [CodeV2]), and it passes through Wrap with its
// code, message and details.
//
// # Error metadata
//
// A v1 *connect.Error carries metadata (Meta) that the transport sends with
// it: the [ReasonHeader] reason and every [WithMetadata] entry ride there.
// A v2 *connect.Error has no metadata; v2 sends headers and trailers through
// the call's *connect.CallInfo instead. [InterceptorV2] bridges the two: it
// writes the verdict's metadata onto the response exactly where v1 put it —
// the response headers for a unary Connect-protocol call, the trailers for
// streaming Connect and for gRPC and gRPC-Web — so a client reads
// x-forge-error-reason from the same place whichever major served it. An
// interceptor that rejects a call before the handler runs never passes back
// through InterceptorV2, so it returns [RejectV2]'s error, which delivers the
// same metadata the same way.
//
// A *connectv2.Error built by [ToConnectV2] keeps that metadata (and the
// whole server-side chain) in an unexported cause, so returning
// ToConnectV2's result from a handler loses nothing: InterceptorV2 reads it
// back, and [ReasonOf] reports it.

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/anypb"
)

// InterceptorV2 is [Interceptor] for connect-go v2: the server interceptor
// that applies [Wrap] to every error a handler returns, unary and streaming
// alike, and emits the result as a *connectv2.Error. Its semantics are v1's,
// so moving a project between majors changes nothing a client reads:
//
//   - An svcerr kind reaches the client with its own code and message,
//     [ReasonHeader] and [WithMetadata] entries as metadata.
//   - An error nothing recognises becomes CodeInternal with
//     [InternalMessage]; its text stays server-side ([Cause]).
//   - A *connect.Error built by this process — of EITHER major — passes
//     through with its code, message, details and metadata, as v1 does.
//   - A *connect.Error a Connect client RETURNED (v2's IsRemote, v1's
//     IsWireError) is re-created as this process's own error with the same
//     code, message and details. v2 would otherwise scrub it to an
//     internal error with no message, where v1 passed it through; the
//     re-creation keeps the v1 wire. As in v1, metadata annotated on a
//     remote error is not sent.
//
// # Installing it: by slot, never by identity
//
// v1 chain builders find a copy of [Interceptor] by comparing interface
// values ([IsInterceptor]) and install exactly one, innermost. A v2
// interceptor is a func, and Go funcs are not comparable, so there is no
// IsInterceptorV2. There does not need to be: InterceptorV2 is idempotent —
// a second copy sees an error the first already converted and passes it
// through with the same verdict, and only the outermost copy running for a
// call writes the verdict's metadata onto it, so nothing is sent twice — so
// a v2 chain builder installs it unconditionally in its innermost slot (the
// last argument to connectv2.NewServer), and a caller that also passed one
// is harmless.
// [WithInterceptorV2] is that append for hand-assembled chains.
//
// Innermost matters for the same reason it does in v1: every interceptor
// outside it — audit, logging, metrics, tracing — then sees the wire error
// rather than the sentinel, and a v2 server never serializes a plain error
// at all (it would send CodeUnknown with no message), so without it a
// handler's `return nil, svcerr.NotFound("x")` loses its code.
func InterceptorV2() connectv2.ServerInterceptor { return interceptV2 }

// WithInterceptorV2 returns extras with [InterceptorV2] appended last — the
// position innermost to the handler in connectv2.NewServer. The input slice
// is not modified. Unlike [WithInterceptor] it cannot remove a copy the
// caller already passed (funcs are not comparable); it does not need to,
// because a second copy is a no-op (see InterceptorV2).
func WithInterceptorV2(extras []connectv2.ServerInterceptor) []connectv2.ServerInterceptor {
	out := make([]connectv2.ServerInterceptor, 0, len(extras)+1)
	out = append(out, extras...)
	return append(out, InterceptorV2())
}

// interceptV2 is a top-level func, not a closure, so every InterceptorV2()
// is the same value.
func interceptV2(next connectv2.ServerFunc) connectv2.ServerFunc {
	return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
		ctx, deliver := claimDelivery(ctx)
		err := next(ctx, spec, stream)
		if err == nil {
			return nil
		}
		ce := ToConnect(err)
		if deliver != nil && !connect.IsWireError(ce) {
			deliverMetadata(deliver, ce.Meta())
		}
		return toV2(err, ce)
	}
}

// RejectV2 is how a connect-go v2 interceptor rejects a call without calling
// next — an authentication failure, a refused credential — so that the
// rejection reaches the wire exactly as returning err from a v1 interceptor
// did. It returns err's verdict, as [ToConnect] reads it, as a
// *connectv2.Error, and writes the verdict's metadata (the [ReasonHeader]
// reason, [WithMetadata] entries, anything on a v1 *connect.Error's Meta)
// onto the call where v1 sends it: the response headers of a unary
// Connect-protocol call, the trailers otherwise. RejectV2(ctx, nil) is nil.
//
// It needs to exist because a v2 error carries no metadata of its own:
// [InterceptorV2] delivers it for errors that come back through it, but a
// call rejected before it reaches the handler never passes through the
// innermost InterceptorV2, so without RejectV2 a v1 error's metadata —
// typically the reason a frontend routes a denial on — is lost.
//
// The verdict is ToConnect's, so the rules are InterceptorV2's: a
// *connect.Error of either major keeps its code, message and details; a
// remote one (v2 IsRemote, v1 IsWireError) is re-created as this process's
// own error, with its metadata withheld as v1 withholds a forwarded error's;
// an error nothing recognises becomes CodeInternal with [InternalMessage].
//
// Metadata is delivered once per call, in the order and multiplicity the
// verdict carries it: when an InterceptorV2 further out in the chain is
// running for this call, RejectV2 leaves the delivery to it (the returned
// error carries the metadata there), so an interceptor sitting between two
// copies does not double a header.
//
// There is no v1 twin: a v1 interceptor returns the *connect.Error and the
// v1 transport sends its Meta.
func RejectV2(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	ce := ToConnect(err)
	if !connect.IsWireError(ce) {
		if info, ok := connectv2.CallInfoForServerContext(ctx); ok && !deliveryClaimed(ctx, info) {
			deliverMetadata(info, ce.Meta())
		}
	}
	return toV2(err, ce)
}

// deliveryOwner is the context key under which an InterceptorV2 records the
// call it delivers error metadata for: the call's *connectv2.CallInfo.
type deliveryOwner struct{}

// claimDelivery makes the caller the deliverer of the call's error metadata
// unless an InterceptorV2 further out already is. It returns the context to
// run the rest of the call with and the CallInfo to deliver to, or nil when
// someone further out delivers (or there is no call to deliver to).
//
// The outermost copy delivers because it sees the error the call actually
// ends with — what v1 sends the metadata of — and delivering once keeps the
// verdict's values exactly as it carries them: a repeated value stays
// repeated, and nothing already on the response is merged away. The key is
// the CallInfo, not a bare flag, so a nested server call that inherits this
// context (an in-process transport) still delivers its own.
func claimDelivery(ctx context.Context) (context.Context, *connectv2.CallInfo) {
	info, ok := connectv2.CallInfoForServerContext(ctx)
	if !ok || deliveryClaimed(ctx, info) {
		return ctx, nil
	}
	return context.WithValue(ctx, deliveryOwner{}, info), info
}

// deliveryClaimed reports whether an InterceptorV2 further out delivers the
// error metadata of the call info describes.
func deliveryClaimed(ctx context.Context, info *connectv2.CallInfo) bool {
	owner, _ := ctx.Value(deliveryOwner{}).(*connectv2.CallInfo)
	return owner != nil && owner == info
}

// deliverMetadata writes a v1 error's metadata onto the v2 call, where the
// v1 transport sends it: merged into the response headers for unary Connect
// (protocol_connect.go mergeResponseHeader), into the end-of-stream metadata
// for streaming Connect, and into the trailers for gRPC and gRPC-Web
// (grpcErrorToTrailer). v2 sends CallInfo's trailers in exactly those last
// two places, so the trailer is the right home for everything but unary
// Connect.
//
// Values are appended, as v1's mergeNonProtocolHeaders appends them: a
// value the response already carries, or one the verdict repeats, is sent
// as many times as v1 sends it. v2's transport drops protocol headers on
// the way out, as v1's merge did. Callers deliver once per call
// (claimDelivery), which is what keeps a second InterceptorV2 from doubling
// a header.
func deliverMetadata(info *connectv2.CallInfo, meta http.Header) {
	if len(meta) == 0 {
		return
	}
	dst := info.ResponseTrailer()
	if info.Spec.StreamType == connectv2.StreamTypeUnary && info.Protocol == connectv2.ProtocolNameConnect {
		dst = info.ResponseHeader()
	}
	for key, values := range meta {
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}

// ToConnectV2 is [ToConnect] for connect-go v2: it converts err into the
// *connectv2.Error a v1 server would have sent for it — the same code,
// message and details. ToConnectV2(nil) is nil.
//
// A local *connectv2.Error is returned unchanged. A remote one (IsRemote:
// it came back from a client call) is re-created as a local error with the
// same code, message and details, which is what v1 sent for a forwarded
// error; returned as-is, v2 would scrub it. Anything else is classified
// exactly as ToConnect classifies it.
//
// v2 errors carry no metadata. The [ReasonHeader] reason and [WithMetadata]
// entries ToConnect would set are kept in the returned error's cause, where
// [InterceptorV2] finds them and writes them onto the response, and where
// [ReasonOf] reads them. The cause also keeps err reachable server-side
// through errors.Is, errors.As, [Cause] and [StackOf].
//
// Handlers normally return [Wrap] instead (InterceptorV2 converts it); this
// is for code that needs the v2 type itself.
func ToConnectV2(err error) *connectv2.Error {
	if err == nil {
		return nil
	}
	return toV2(err, ToConnect(err))
}

// CodeV2 is [Code] as a connect-go v2 code: the code a client is told for
// err, read through either major's *connect.Error or classified from an
// svcerr kind. Assert it in a test that calls a v2 handler directly, where
// no interceptor runs: connectv2.CodeOf reads a bare kind — and a
// *connect.Error of the other major — as CodeUnknown.
func CodeV2(err error) connectv2.Code { return connectv2.Code(Code(err)) }

// NewV2 is [New] for a connect-go v2 code: a classified error from a wire
// code known only at runtime and deliberate public detail. The codes of the
// two majors share their numeric values.
func NewV2(code connectv2.Code, detail string) error { return New(connect.Code(code), detail) }

// toV2 re-encodes ce — ToConnect(err) — as a *connectv2.Error. A local v2
// error handed in directly is already that encoding.
func toV2(err error, ce *connect.Error) *connectv2.Error {
	if e, ok := err.(*connectv2.Error); ok && e != nil && !e.IsRemote() {
		return e
	}
	out := connectv2.NewError(connectv2.Code(ce.Code()), ce.Message())
	for _, d := range ce.Details() {
		out = out.WithDetail(&connectv2.ErrorDetail{Type: d.Type(), Value: d.Bytes()})
	}
	vc := &v2Cause{err: ce}
	if !connect.IsWireError(ce) && len(ce.Meta()) > 0 {
		vc.meta = ce.Meta().Clone()
	}
	return out.WithCause(vc)
}

// fromV2 is the v1 encoding of a v2 verdict: the same code, message and
// details. A remote v2 error stays a remote (wire) v1 error, so its metadata
// is withheld the way v1 withholds a forwarded error's. Metadata a previous
// toV2 kept in the cause comes back as Meta.
func fromV2(e *connectv2.Error) *connect.Error {
	msg := &v2Message{err: e}
	var ce *connect.Error
	if e.IsRemote() {
		ce = connect.NewWireError(connect.Code(e.Code()), msg)
	} else {
		ce = connect.NewError(connect.Code(e.Code()), msg)
	}
	for _, d := range e.Details() {
		typeURL := d.Type
		if !strings.Contains(typeURL, "/") {
			typeURL = "type.googleapis.com/" + typeURL
		}
		if cd, err := connect.NewErrorDetail(&anypb.Any{TypeUrl: typeURL, Value: d.Value}); err == nil {
			ce.AddDetail(cd)
		}
	}
	if vc, ok := e.Unwrap().(*v2Cause); ok {
		for key, values := range vc.meta {
			ce.Meta()[key] = append(ce.Meta()[key], values...)
		}
	}
	return ce
}

// v2Message is the underlying error of fromV2's v1 encoding: Error() is the
// v2 error's message — which is what v1's Message() publishes — and Unwrap
// keeps the v2 error, and everything behind it, on the chain.
type v2Message struct{ err *connectv2.Error }

func (m *v2Message) Error() string { return m.err.Message() }
func (m *v2Message) Unwrap() error { return m.err }

// v2Cause is the cause toV2 attaches: the complete v1 verdict the v2 error
// encodes, and its metadata. v2 never serializes a cause, so it is invisible
// on the wire.
//
// Unwrap leads to the verdict, so errors.Is, Cause, StackOf and Classify see
// everything the v1 error carries. The metadata walks (reasonOf, metadataOf)
// stop here instead: meta is already the verdict's complete metadata, with
// the nearest annotation of each key resolved when it was built, and walking
// on would let an annotation that verdict overrode win again.
type v2Cause struct {
	meta http.Header
	err  *connect.Error
}

func (c *v2Cause) Error() string { return c.err.Message() }
func (c *v2Cause) Unwrap() error { return c.err }

// connectVerdict is the nearest *connect.Error of either major on an error's
// client view; exactly one field is set.
type connectVerdict struct {
	v1 *connect.Error
	v2 *connectv2.Error
}

// code is the verdict's code in v1 terms (the two majors share values).
func (v connectVerdict) code() connect.Code {
	if v.v2 != nil {
		return connect.Code(v.v2.Code())
	}
	return v.v1.Code()
}

// visibleConnect is visibleAs[*connect.Error] for both majors at once: the
// nearest *connect.Error of either major, so a chain holding both resolves
// to the one a reader meets first, as errors.As would. For a chain with no
// v2 error it finds exactly what visibleAs[*connect.Error] finds.
func visibleConnect(err error) (connectVerdict, bool) {
	var found connectVerdict
	ok := walkVisible(err, func(e error) bool {
		switch t := e.(type) {
		case *connect.Error:
			found = connectVerdict{v1: t}
			return true
		case *connectv2.Error:
			found = connectVerdict{v2: t}
			return true
		case *redactedError:
			return false
		}
		x, ok := e.(interface{ As(any) bool })
		if !ok {
			return false
		}
		var v1 *connect.Error
		if x.As(&v1) {
			found = connectVerdict{v1: v1}
			return true
		}
		var v2 *connectv2.Error
		if x.As(&v2) {
			found = connectVerdict{v2: v2}
			return true
		}
		return false
	})
	return found, ok
}
