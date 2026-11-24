package serverkit

import (
	"context"
	"log/slog"
	"sync"

	connectv2 "connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// StreamDrainInterceptorV2 is StreamDrainInterceptor for a connect-go v2
// server: the same client contract (stream_drain.go) — a server-streaming or
// bidi handler is bound to the drain of the listener serving it, and a stream
// the drain ended closes OK with DrainedTrailer, or Unavailable for a
// procedure RedialOnDrain names. Every streaming span is stamped with
// StreamTypeAttr; unary RPCs pass through untouched. Put it inner to logging
// and metrics, as NewChainV2 does.
func StreamDrainInterceptorV2() connectv2.ServerInterceptor {
	var warn sync.Once
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			if st := streamTypeNameV2(spec.StreamType); st != "" {
				trace.SpanFromContext(ctx).SetAttributes(attribute.String(StreamTypeAttr, st))
			}
			if spec.StreamType != connectv2.StreamTypeServer && spec.StreamType != connectv2.StreamTypeBidi {
				return next(ctx, spec, stream)
			}
			streams := StreamsFrom(ctx)
			if streams == nil {
				warn.Do(func() {
					slog.WarnContext(ctx, "serverkit.StreamDrainInterceptorV2: no drain registry installed for this server; streams will not be ended at shutdown",
						"procedure", spec.Procedure, "hint", "serve through serverkit.NewHTTPServer, or wrap the handler with Streams.Install")
				})
				return next(ctx, spec, stream)
			}
			ctx, release := streams.Bind(ctx)
			defer release()
			err := next(ctx, spec, stream)
			if !Ended(ctx) || !isCancellationV2(err) {
				return err
			}
			if info, ok := connectv2.CallInfoForServerContext(ctx); ok {
				info.ResponseTrailer().Set(DrainedTrailer, "true")
			}
			if streams.redials(spec.Procedure) {
				return svcerr.ToConnectV2(svcerr.ErrServerDraining)
			}
			return nil
		}
	}
}

func streamTypeNameV2(t connectv2.StreamType) string {
	switch t {
	case connectv2.StreamTypeServer:
		return "server"
	case connectv2.StreamTypeClient:
		return "client"
	case connectv2.StreamTypeBidi:
		return "bidi"
	}
	return ""
}

// isCancellationV2 is isCancellation, reading a v2 error's code too.
func isCancellationV2(err error) bool {
	return isCancellation(err) || connectv2.CodeOf(err) == connectv2.CodeCanceled
}
