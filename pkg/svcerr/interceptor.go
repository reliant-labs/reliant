package svcerr

import (
	"context"

	"connectrpc.com/connect"
)

// Interceptor returns the Connect interceptor that applies [Wrap] to every
// error a handler returns, so `return nil, svcerr.PermissionDenied("…")`
// reaches the client as CodePermissionDenied. connect-go itself turns any
// error that is not already a *connect.Error into CodeUnknown, and the
// svcerr constructors build sentinel errors, not Connect ones.
//
// Wrap is idempotent: a handler that already wrapped its error (or built a
// *connect.Error itself) is passed through unchanged, so writing
// `svcerr.Wrap` in a handler stays correct. An error nothing recognises
// becomes CodeInternal with [InternalMessage], never its raw text.
//
// # What it redacts, and what it does not
//
// Redaction applies to what svcerr classifies: an unrecognised error, and
// any text wrapped around a kind or hidden behind [WithCause]. A
// *connect.Error is not redacted — it passes through with its code and
// message exactly as built. That is deliberate, not an oversight:
//
//   - A *connect.Error is the handler's explicit verdict, and connect-go
//     publishes its Message() with or without this interceptor. Rewriting it
//     here would mean guessing, from the error's type, which text someone
//     meant a client to read: `connect.NewError(code, fmt.Errorf("push
//     rejected: %w", err))` carries a driver's DSN in one handler and the
//     user's own git rejection — the actionable part of the toast — in
//     another, and they are the same Go type.
//   - Changing it here would change what every project's clients read the
//     moment it bumped forge, with no diff in the project to review.
//
// So `connect.NewError(code, err)` publishes err.Error() — the leak class
// svcerr exists to close — and the fix is at the call site: return a kind
// (`svcerr.WrapOr(err, svcerr.Internal("push failed"))`), with any text a
// client should read in the constructor's argument
// (`svcerr.FailedPrecondition(daemonMsg)`). forgeconv-handler-connect-error
// lists every connect.NewError in a handler package, and `forge fix
// connect-errors` rewrites the ones with fixed text.
//
// Forge's chain builders (observe.Chain, observe.DefaultMiddlewares and
// serverkit.NewChain) install it innermost, next to the handler, so every
// interceptor outside it — audit, logging, metrics — sees the wire error
// rather than the sentinel. A project that assembles a chain by hand adds it
// last; passing it to a builder as well is harmless, since the builders
// replace a copy they find with their own innermost one (see [IsInterceptor]).
func Interceptor() connect.Interceptor { return interceptor{} }

// IsInterceptor reports whether i is the interceptor [Interceptor] returns.
// Chain builders use it to install exactly one, innermost, however many the
// caller passed.
func IsInterceptor(i connect.Interceptor) bool {
	_, ok := i.(interceptor)
	return ok
}

// WithInterceptor returns extras with every [Interceptor] removed and one
// appended last — the position innermost to the handler. The input slice is
// not modified.
func WithInterceptor(extras []connect.Interceptor) []connect.Interceptor {
	out := make([]connect.Interceptor, 0, len(extras)+1)
	for _, i := range extras {
		if !IsInterceptor(i) {
			out = append(out, i)
		}
	}
	return append(out, Interceptor())
}

type interceptor struct{}

func (interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		if err != nil {
			return resp, Wrap(err)
		}
		return resp, nil
	}
}

func (interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return Wrap(next(ctx, conn))
	}
}
