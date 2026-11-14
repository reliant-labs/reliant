package crud

import (
	"context"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// An ORM call's span is a child of the caller's, and a call with no caller
// span opens none — the policy observe/dbtrace already applies to the SQL
// beneath it. A parentless `orm.<Op><Entity>` span is a one-span root trace
// that names a table and nothing about why it was touched (Reliant's worker
// emitted a stream of them from writes detached with context.Background()).
func TestRepoSpanOpensOnlyUnderACallerSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	// repoTracer is resolved from the global provider once, and the global
	// delegates to the FIRST provider ever set, so swapping the global would
	// leave a repeat run (-count, -cpu) recording into the previous run's
	// provider. Swap the package tracer itself.
	prev := repoTracer
	repoTracer = tp.Tracer("orm")
	t.Cleanup(func() { repoTracer = prev })

	repo := NewRepo[plainRow]()
	repo.ensureMeta(dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())})

	_, orphan := repo.startSpan(context.Background(), "Create")
	if orphan.SpanContext().IsValid() {
		t.Error("an ORM call with no caller span opened a root span")
	}
	orphan.End()
	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("recorded %d spans for a parentless ORM call, want 0", n)
	}

	ctx, parent := tp.Tracer("test").Start(context.Background(), "RunActivity")
	_, child := repo.startSpan(ctx, "Create")
	child.End()
	parent.End()

	var found bool
	for _, s := range rec.Ended() {
		if s.Name() == "orm.CreateplainRow" {
			found = true
			if s.Parent().SpanID() != parent.SpanContext().SpanID() {
				t.Errorf("orm span parent = %s, want the caller's %s", s.Parent().SpanID(), parent.SpanContext().SpanID())
			}
		}
	}
	if !found {
		names := []string{}
		for _, s := range rec.Ended() {
			names = append(names, s.Name())
		}
		t.Errorf("no orm.CreateplainRow span under the caller's span; spans: %q", names)
	}
}
