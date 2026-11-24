// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/telemetry"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// runTraceContext is the context a run's Temporal start is issued from, and
// the trigger the run carries.
//
// The Temporal tracing interceptor (forge's temporalotel, on the client)
// parents the started workflow under whatever span ctx holds. For an attended
// launch that is right: the run belongs to the request of the person who
// started it, so ctx and trigger come back unchanged.
//
// An unattended launch (a stored trigger fired) is different. One integration
// delivery or run event fans out to the triggers of several orgs, so
// parenting each run under the span that fired it puts one trace across
// several orgs. Each such run is a trace of its own instead: the start is
// issued from ctx with an empty span context (and no baggage, which belongs
// to the firing trace too), so the interceptor opens a new root, and the
// returned trigger carries firingTraceParent for the run to link back to
// (TriggerInfo.TraceParent, linked by the runtime's startRunTelemetry).
// ctx's deadline and values are kept.
func runTraceContext(ctx context.Context, trigger *v2.TriggerInfo, firingTraceParent string) (context.Context, *v2.TriggerInfo) {
	if trigger == nil || !core.TriggerEventKind(trigger.Kind).Unattended() {
		return ctx, trigger
	}
	runCtx := baggage.ContextWithoutBaggage(trace.ContextWithSpanContext(ctx, trace.SpanContext{}))
	linked := *trigger
	linked.TraceParent = ""
	if telemetry.SpanContextOf(firingTraceParent).IsValid() {
		linked.TraceParent = firingTraceParent
	}
	return runCtx, &linked
}
