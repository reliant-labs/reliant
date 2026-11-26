// File: root.go — a trace per unit of background work.
//
// A request gets its trace from the Connect edge. A loop iteration, a
// reconcile or a scheduled job has no edge: its work runs under whatever
// context the loop was started with, which carries no span. Everything it then
// does — the decorated business methods, the SQL, the outbound calls — either
// opens nothing (dbtrace opens spans only under a parent) or opens a one-span
// trace of its own per call. In the control plane, `domainregistry.ListConverging`
// ran 836 times a day, every one a root with no children.
//
// RunRoot is the missing edge: one root span per unit of work, so its calls
// nest under it as they would under an RPC.

package observe

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// RootTracerName is the instrumentation scope of RunRoot's spans.
const RootTracerName = "github.com/reliant-labs/forge/pkg/observe/root"

// RunRoot runs fn under a NEW ROOT span named name and ends it when fn
// returns. It is a new trace even when ctx already carries a span: a loop
// started inside a request must not file every later iteration under that
// request. The span records fn's error, and a panic in fn — recorded, then
// re-raised, so the caller's recovery policy still applies.
//
// Name it for the work, not the instance: "worker.metering.tick",
// "reconcile Workspace". Put ids in attributes (opts), never in the name.
func RunRoot(ctx context.Context, name string, fn func(context.Context) error, opts ...trace.SpanStartOption) (err error) {
	opts = append([]trace.SpanStartOption{trace.WithNewRoot()}, opts...)
	ctx, span := otel.Tracer(RootTracerName).Start(ctx, name, opts...)
	defer func() {
		if r := recover(); r != nil {
			RecordSpanError(span, fmt.Errorf("panic: %v", r))
			span.End()
			panic(r)
		}
		RecordSpanError(span, err)
		span.End()
	}()
	return fn(ctx)
}
