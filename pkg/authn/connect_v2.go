package authn

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	connectv2proto "connectrpc.com/connect/v2/connectproto"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// This file is the connect-go v2 surface of the package. Every v2 symbol
// resolves the same Policy through the same code as its v1 twin, so the two
// majors make identical authentication decisions and put identical codes and
// messages on the wire; only the connect types differ.

// NewInterceptorV2 is [NewInterceptor] for connect-go v2: it resolves p in
// the same three modes, refuses the same policies at construction, and
// returns a [connectv2.ServerInterceptor] that covers unary and streaming
// RPCs alike. In passthrough mode (Policy.ExternalAuth) it returns an
// interceptor that calls next untouched.
//
// A v2 interceptor is a func, so it cannot carry the Claims method
// serverkit.AuthFor reads off the v1 interceptor; [Policy.ClaimsReader]
// answers the same question from the Policy.
//
// Rejections reach the wire exactly as the v1 interceptor's do: the same
// code and the same message ("missing Authorization header (…)", "invalid
// authorization format", "invalid token: <err>", "identity enrichment
// failed: <err>"), built as v2 *connect.Error values, because a v2 transport
// sends any other error as a bare CodeUnknown. An Enrich or MapError hook
// may return a *connect.Error of either major; it is passed through with
// its code, message and details, and its error metadata — the
// x-forge-error-reason of a svcerr.WithReason verdict, anything on a v1
// error's Meta — is sent where v1 sends it ([svcerr.RejectV2]). An error
// the hook received from a client call (a remote one) is re-created as this
// server's own with the same code, message and details, and its metadata
// withheld, as v1 sends a forwarded error; v2 would otherwise scrub it to
// an internal error with no message.
func NewInterceptorV2(p Policy) (connectv2.ServerInterceptor, error) {
	ic, err := newInterceptor(p, "authn.NewInterceptorV2")
	if err != nil {
		return nil, err
	}
	if ic == nil {
		return func(next connectv2.ServerFunc) connectv2.ServerFunc { return next }, nil
	}
	ic.v2 = true
	return ic.wrapV2, nil
}

func (a *interceptor) wrapV2(next connectv2.ServerFunc) connectv2.ServerFunc {
	return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
		if a.allowUnauthenticated(spec.Procedure) {
			return next(ctx, spec, stream)
		}
		authed, err := a.authenticate(ctx, a.policy.authorization(requestHeaderV2(ctx)))
		if err != nil {
			return errorToV2(ctx, err)
		}
		return next(authed, spec, stream)
	}
}

// ClaimsReader is how to read back the caller an interceptor built from p
// admitted — the connect-free form of the v1 interceptor's Claims method,
// and what a connect-go v2 chain passes as its caller lookup (serverkit's
// v2 auth slot cannot read it off a func). It is [ClaimsFromContext] when p
// resolves to Validate mode with the library's own stash (ContextWithClaims
// nil), and nil otherwise: passthrough stashes nothing, and a project-owned
// ContextWithClaims has a reader only the project knows.
func (p Policy) ClaimsReader() func(context.Context) (*Claims, bool) {
	if p.resolve() == modeValidate && p.ContextWithClaims == nil {
		return ClaimsFromContext
	}
	return nil
}

// GetUserV2 is [GetUser] for a connect-go v2 handler: the same lookup, and
// when the context carries no principal the same rejection —
// CodeUnauthenticated, message "no authenticated user" — as a v2
// *connect.Error, which a v2 transport serializes. (GetUser's v1 error is
// foreign to a v2 transport, which would send it as a bare CodeUnknown
// unless an interceptor such as svcerr's v2 one re-creates it.)
func GetUserV2(ctx context.Context) (*Claims, error) {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return nil, connectv2.NewError(connectv2.CodeUnauthenticated, "no authenticated user")
	}
	return claims, nil
}

// mapValidationError runs the policy's error-mapping hook over a validation
// failure. The interceptor's own major's hook wins; the other major's hook
// is the fallback, its result converted by the wrapper that returns it.
// With no hook, or a hook returning nil, fallback stands.
func (a *interceptor) mapValidationError(err error, fallback *connect.Error) error {
	v1Hook := func() error {
		if mapped := a.policy.MapError(err, fallback); mapped != nil {
			return mapped
		}
		return fallback
	}
	v2Hook := func() error {
		fallback2 := toV2Error(fallback, fallback)
		if mapped := a.policy.MapErrorV2(err, fallback2); mapped != nil {
			return mapped
		}
		return fallback2
	}
	switch {
	case a.v2 && a.policy.MapErrorV2 != nil:
		return v2Hook()
	case a.policy.MapError != nil:
		return v1Hook()
	case a.policy.MapErrorV2 != nil:
		return v2Hook()
	}
	return fallback
}

// requestHeaderV2 returns the v2 call's request headers as an http.Header,
// the shape Policy.ExtractToken and the Authorization read take. A context
// that did not come through a connectv2.Server has no headers.
func requestHeaderV2(ctx context.Context) http.Header {
	info, ok := connectv2.CallInfoForServerContext(ctx)
	if !ok {
		return http.Header{}
	}
	h := info.RequestHeader()
	out := make(http.Header, h.Len())
	for k, vs := range h.All() {
		out[k] = vs
	}
	return out
}

// outermostConnectError finds the first *connect.Error of either major in
// err's tree, in errors.As order, and returns it in its own major's slot.
// It decides which major's verdict an error carries when a v2 error wraps a
// v1 one (or the reverse) as its cause.
func outermostConnectError(err error) (*connect.Error, *connectv2.Error) {
	switch e := err.(type) {
	case nil:
		return nil, nil
	case *connect.Error:
		return e, nil
	case *connectv2.Error:
		return nil, e
	case interface{ Unwrap() error }:
		return outermostConnectError(e.Unwrap())
	case interface{ Unwrap() []error }:
		for _, inner := range e.Unwrap() {
			if e1, e2 := outermostConnectError(inner); e1 != nil || e2 != nil {
				return e1, e2
			}
		}
	}
	return nil, nil
}

// errorToV1 makes an error this package returns carry its verdict to a v1
// wire. A v1 *connect.Error (and any plain error) is returned unchanged; a
// v2 one is re-created with the same code, message and details.
func errorToV1(err error) error {
	e1, e2 := outermostConnectError(err)
	if e1 != nil || e2 == nil {
		return err
	}
	out := connect.NewError(connect.Code(e2.Code()), &messageError{msg: e2.Message(), cause: err})
	for _, d := range e2.Details() {
		if d1, derr := connect.NewErrorDetail(connectv2proto.ErrorDetailToAny(d)); derr == nil {
			out.AddDetail(d1)
		}
	}
	return out
}

// errorToV2 makes an authentication rejection reach a v2 wire as the v1
// interceptor's reaches a v1 wire, and is what wrapV2 returns. The verdict
// is the outermost *connect.Error of either major in err — the one v1's
// transport finds with errors.As — and it goes out through
// svcerr.RejectV2: the same code, message and details, its metadata on the
// call's headers or trailers where v1 sends a *connect.Error's Meta, and a
// remote verdict re-created as a local one (metadata withheld). Any other
// error becomes CodeUnknown with its text, which is what a v1 transport sent
// for a plain error.
func errorToV2(ctx context.Context, err error) error {
	e1, e2 := outermostConnectError(err)
	switch {
	case e2 != nil:
		return svcerr.RejectV2(ctx, e2)
	case e1 != nil:
		return svcerr.RejectV2(ctx, e1)
	case err == nil:
		return nil
	}
	return connectv2.NewError(connectv2.CodeUnknown, err.Error()).WithCause(err)
}

// toV2Error re-creates e1 as a v2 *connect.Error with the same code, message
// and details, keeping cause (usually e1 or the error wrapping it) as the
// local cause so errors.Is still finds what it wrapped.
func toV2Error(e1 *connect.Error, cause error) *connectv2.Error {
	out := connectv2.NewError(connectv2.Code(e1.Code()), e1.Message()).WithCause(cause)
	for _, d := range e1.Details() {
		out = out.WithDetail(&connectv2.ErrorDetail{Type: d.Type(), Value: d.Bytes()})
	}
	return out
}

// messageError is the underlying error of a v1 *connect.Error re-created
// from a v2 one: its text is the wire message, and it unwraps to the
// original so errors.Is still finds the v2 error and what it wrapped.
type messageError struct {
	msg   string
	cause error
}

func (e *messageError) Error() string { return e.msg }
func (e *messageError) Unwrap() error { return e.cause }
