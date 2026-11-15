// Package temporalotel carries trace context through Temporal, so the request
// that starts or signals a workflow, the workflow, and every activity it runs
// are ONE trace.
//
// It is a thin layer over Temporal's own go.temporal.io/sdk/contrib/opentelemetry
// interceptor, with forge's defaults:
//
//   - the global OpenTelemetry propagator (W3C trace-context + baggage, which
//     observe.Setup installs), so the context Temporal carries is the same one
//     Connect, HTTP and NATS carry — not the contrib's private default;
//   - the global tracer provider, read at span time, so an interceptor built
//     before observe.Setup still reports;
//   - AllowInvalidParentSpans, so workflows already in flight under a previous
//     (or no) tracing scheme keep running instead of failing on a header they
//     cannot parse.
//
// Wire it ONCE, on the client: a client interceptor that is also a worker
// interceptor — this one is — is applied to every worker created from that
// client, so StartWorkflow/SignalWithStart inject the caller's span into the
// workflow header and the worker continues it.
//
//	ti, err := temporalotel.NewInterceptor(temporalotel.Options{})
//	c, err := client.Dial(client.Options{Interceptors: []interceptor.ClientInterceptor{ti}})
//
// Workflow spans are created by the SDK's replay-aware machinery, so they are
// safe in the deterministic workflow sandbox.
package temporalotel

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
)

// TracerName is the instrumentation scope of the spans this package creates.
const TracerName = "github.com/reliant-labs/forge/pkg/observe/temporalotel"

// Options tunes the interceptor. The zero value is forge's default.
type Options struct {
	// DisableSignalTracing, DisableQueryTracing and DisableUpdateTracing stop
	// spans for those calls; queries in particular can be polled hot.
	DisableSignalTracing bool
	DisableQueryTracing  bool
	DisableUpdateTracing bool
}

// NewInterceptor returns the tracing interceptor for both the Temporal client
// and its workers. See the package doc for where to install it.
func NewInterceptor(opts Options) (interceptor.Interceptor, error) {
	i, err := opentelemetry.NewTracingInterceptor(tracerOptions(opts))
	if err != nil {
		return nil, fmt.Errorf("temporal tracing interceptor: %w", err)
	}
	return i, nil
}

func tracerOptions(opts Options) opentelemetry.TracerOptions {
	return opentelemetry.TracerOptions{
		// otel.Tracer delegates to whatever provider is installed when a span
		// starts, and otel.GetTextMapPropagator likewise.
		Tracer:                  otel.Tracer(TracerName),
		TextMapPropagator:       otel.GetTextMapPropagator(),
		AllowInvalidParentSpans: true,
		DisableSignalTracing:    opts.DisableSignalTracing,
		DisableQueryTracing:     opts.DisableQueryTracing,
		DisableUpdateTracing:    opts.DisableUpdateTracing,
	}
}
