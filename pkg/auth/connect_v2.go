package auth

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
)

// ConnectInterceptorV2 is [Validator.ConnectInterceptor] for connect-go v2:
// the same server-side interceptor as a [connectv2.ServerInterceptor]. One
// function covers unary and every streaming shape — a v2 interceptor wraps
// the whole server call — so there is no v2 twin of the unary-only
// [Validator.Interceptor]; that entrypoint exists only for projects that
// wired it before streaming was covered.
//
// The decisions are identical to the v1 interceptor's: the same skip list
// ([Validator.IsUnauthenticatedProcedure], matched on Spec.Procedure), the
// same headers ([CallInfo.RequestHeader]), the same [Validator.AuthenticateHeaders]
// verdict, the same claims stash. A rejection reaches the wire with the
// code AND message the v1 interceptor sends: AuthenticateHeaders reports a
// v1 *connect.Error, which is re-created here as a v2 *connect.Error
// (a v2 transport would otherwise send a foreign error as a bare
// CodeUnknown). Error metadata (v1 Error.Meta) is not carried; nothing in
// this package sets any.
//
// [CallInfo.RequestHeader]: https://pkg.go.dev/connectrpc.com/connect/v2#CallInfo.RequestHeader
func (v *Validator) ConnectInterceptorV2(opts InterceptorOptions, withClaims func(context.Context, *Claims) context.Context) connectv2.ServerInterceptor {
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			if v.IsUnauthenticatedProcedure(spec.Procedure, opts.SkipMethods) {
				return next(ctx, spec, stream)
			}
			claims, err := v.AuthenticateHeaders(ctx, requestHeaderV2(ctx), opts)
			if err != nil {
				return errorToV2(err)
			}
			if claims != nil && withClaims != nil {
				ctx = withClaims(ctx, claims)
			}
			return next(ctx, spec, stream)
		}
	}
}

// requestHeaderV2 returns the v2 call's request headers as an http.Header,
// the shape AuthenticateHeaders reads. A context that did not come through a
// connectv2.Server (a raw ServerFunc call) has no headers.
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

// errorToV2 makes an error this package produced reach a v2 wire as the v1
// interceptor's does. A v2 *connect.Error is returned as is; a v1 one is
// re-created with the same code, message and details (the original kept as
// the local cause, so errors.Is still finds what it wrapped); any other
// error becomes CodeUnknown with its text, which is what a v1 handler sent
// for a plain error.
func errorToV2(err error) error {
	e1, e2 := outermostConnectError(err)
	switch {
	case e2 != nil:
		return err
	case e1 != nil:
		out := connectv2.NewError(connectv2.Code(e1.Code()), e1.Message()).WithCause(err)
		for _, d := range e1.Details() {
			out = out.WithDetail(&connectv2.ErrorDetail{Type: d.Type(), Value: d.Bytes()})
		}
		return out
	case err == nil:
		return nil
	}
	return connectv2.NewError(connectv2.CodeUnknown, err.Error()).WithCause(err)
}

// outermostConnectError finds the first *connect.Error of either major in
// err's tree, in errors.As order, and returns it in its own major's slot.
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
