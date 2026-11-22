package observe

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// attrForgeProbe is the span attribute that marks a request as a probe:
// release.ProbeAttr, declared again here so a workload does not import the
// release-shape package for one string (probe_mark_test.go holds them equal).
// Readers keep marked spans out of real traffic (obsquery.SpanNotProbeSQL).
const attrForgeProbe = "forge.probe"

// MarkProbeSpans marks the request in ctx as a probe: forge.probe=true on its
// server span (the one RequestSpanInterceptor recorded) and on the innermost
// span, so release health, RED and the issue views leave it out while trace
// search still finds it.
//
// Call it only from a handler whose every caller is a probe, such as the
// health rpc: the mark takes a request out of every error rate, so a handler
// that real callers reach must never set it on their behalf.
func MarkProbeSpans(ctx context.Context) {
	mark := attribute.Bool(attrForgeProbe, true)
	server := serverSpanOf(ctx)
	server.SetAttributes(mark)
	// Compare by SpanContext, never `inner != server`: a span is an
	// interface, and otel's non-recording spans (what every span is with no
	// tracer provider installed) hold a TraceState with a slice in it, so
	// comparing two of them panics.
	if inner := trace.SpanFromContext(ctx); !inner.SpanContext().Equal(server.SpanContext()) {
		inner.SetAttributes(mark)
	}
}

// HealthCheckProcedure is the gRPC health check serverkit serves.
const HealthCheckProcedure = "/grpc.health.v1.Health/Check"

// HeaderHealthProbe is the request header a probe sets on a health check to
// say it is one (`forge env up` sends "env-up"). Honoured only on
// HealthCheckProcedure: a monitor's own health checks, which carry no such
// header, are real traffic and keep their metrics.
const HeaderHealthProbe = "Forge-Health-Probe"

// IsHealthProbe reports whether a call to procedure with header h is a
// declared probe: the gRPC health check carrying HeaderHealthProbe.
// MetricsInterceptor records no rpc.server.* sample for it, and serverkit's
// health handler marks its spans (MarkProbeSpans). Setting the header can
// hide nothing but a health check.
func IsHealthProbe(procedure string, h http.Header) bool {
	return isHealthProbe(procedure, h)
}

// isHealthProbe is IsHealthProbe over either connect major's request header.
func isHealthProbe(procedure string, h interface{ Get(string) string }) bool {
	return procedure == HealthCheckProcedure && h.Get(HeaderHealthProbe) != ""
}
