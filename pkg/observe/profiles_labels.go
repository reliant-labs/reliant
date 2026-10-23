// File: profiles_labels.go — tying CPU samples to the span that was running.
//
// pprof labels follow a goroutine (and the goroutines it starts) for as long as
// its context carries them. TracingInterceptor wraps each RPC in labels naming
// the span it just started (ProfileLabelsMiddleware does the same for plain
// net/http handlers), the profiler lifts them into OTLP
// links, and "which code burned CPU during THIS slow request" becomes a lookup
// by trace id. The cost is one label set per request, and only while profiling
// runs in this process.
//
// The CPU profiler samples at 100 Hz, so a span shorter than ~10 ms usually
// collects no samples. Linking is for slow spans, which is when it is wanted.

package observe

import (
	"context"
	"net/http"
	"runtime/pprof"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"
)

// withSpanProfileLabels runs fn with pprof labels naming the span in ctx, when
// profiling is active and the span is valid. Otherwise it just calls fn.
func withSpanProfileLabels(ctx context.Context, fn func(context.Context)) {
	if !profilingActive.Load() {
		fn(ctx)
		return
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		fn(ctx)
		return
	}
	pprof.Do(ctx, pprof.Labels(profileLabelTraceID, sc.TraceID().String(), profileLabelSpanID, sc.SpanID().String()), fn)
}

// profileLabelsInterceptor labels the goroutine serving an RPC with the span
// already in its context. It exists for a span opened by an interceptor that
// knows nothing about profiling (otelconnect, via Deps.Trace); Chain places it
// directly inside that interceptor. TracingInterceptor does the same for the
// span it opens itself.
func profileLabelsInterceptor() connect.Interceptor { return profileLabels{} }

type profileLabels struct{}

func (profileLabels) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (resp connect.AnyResponse, err error) {
		withSpanProfileLabels(ctx, func(ctx context.Context) { resp, err = next(ctx, req) })
		return resp, err
	}
}

func (profileLabels) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (profileLabels) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) (err error) {
		withSpanProfileLabels(ctx, func(ctx context.Context) { err = next(ctx, conn) })
		return err
	}
}

// ProfileLabelsMiddleware labels the goroutine serving a plain net/http request
// with its span, for handlers that are not Connect RPCs (TracingInterceptor
// already covers those). Mount it
// INSIDE the otelhttp handler (or anything else that starts the server span).
func ProfileLabelsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		withSpanProfileLabels(r.Context(), func(ctx context.Context) {
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
}
