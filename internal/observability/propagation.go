// Copyright (c) 2025 Reliant Labs
package observability

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// InstallPropagation installs the W3C trace-context + baggage propagator
// globally, the one forge's observe.Setup installs in the server roles, and
// nothing else: no tracer provider, no exporter.
//
// It is the daemon's whole tracing setup. The daemon exports no spans (a
// local daemon must send nothing off the user's machine, and cloud daemons
// have no OTLP credential yet — research/tracing/AUDIT.md, Part 5), but it
// still continues the trace context the gateway hands it with each tool
// request, so its log lines carry the dispatching request's trace_id. Without
// a propagator the global one is a no-op and that context is dropped.
func InstallPropagation() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}
