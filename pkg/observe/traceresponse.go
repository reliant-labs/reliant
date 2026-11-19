// File: traceresponse.go — tell the caller which trace its request landed in.
//
// A browser sends a traceparent on every call (web-runtime trace.ts), but the
// trace the server records is not always the one the browser named: a chain
// built without otelconnect.WithTrustRemote starts a fresh trace, a proxy can
// restart it, and the sampled flag the browser chose is not the decision the
// server made. So the browser could not reliably say "this failed request is
// trace X". The W3C Trace Context Level 2 `traceresponse` header is the answer:
// the server returns the context of its own server span, and the frontend
// runtime stamps that trace id on the error it raises, the dev log line and
// the exception it reports — the join from a screenshot of an error to the
// trace that explains it.

package observe

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"
)

// TraceResponseHeader is the W3C Trace Context Level 2 response header:
// `00-<trace-id>-<span-id>-<flags>`, naming the server span.
const TraceResponseHeader = "Traceresponse"

// TraceResponseValue renders sc as a traceresponse value, or "" when sc is not
// a valid span context.
func TraceResponseValue(sc trace.SpanContext) string {
	if !sc.IsValid() {
		return ""
	}
	return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + sc.TraceFlags().String()
}

// TraceResponseInterceptor writes the traceresponse header for the server span
// on ctx. Place it directly inside the interceptor that opens that span —
// observe.Chain does — so the header names the span every log line and child
// span of the request hangs off.
//
// Unary: on the response, and on a *connect.Error's metadata, which Connect
// sends as response headers, so a FAILED call names its trace too (the case
// that matters). An error that is not a *connect.Error is left untouched rather
// than rewrapped; forge handlers return svcerr-wrapped connect errors.
// Streaming: on the response headers before the handler runs.
func TraceResponseInterceptor() connect.Interceptor { return traceResponseInterceptor{} }

type traceResponseInterceptor struct{}

func (traceResponseInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		v := TraceResponseValue(trace.SpanContextFromContext(ctx))
		if v == "" || req.Spec().IsClient {
			return resp, err
		}
		if err != nil {
			var ce *connect.Error
			if errors.As(err, &ce) {
				ce.Meta().Set(TraceResponseHeader, v)
			}
			return resp, err
		}
		// A handler that succeeded returned a real response; only an erroring
		// one returns a typed nil boxed in the interface (see
		// requestIDInterceptor), and that path returned above.
		if resp != nil {
			resp.Header().Set(TraceResponseHeader, v)
		}
		return resp, err
	}
}

func (traceResponseInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (traceResponseInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if v := TraceResponseValue(trace.SpanContextFromContext(ctx)); v != "" {
			conn.ResponseHeader().Set(TraceResponseHeader, v)
		}
		return next(ctx, conn)
	}
}
