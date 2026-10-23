package dbtrace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// The pgx pool's equivalent of TestQueriesAreChildSpansWithSanitizedTextAndNoArgs:
// a project that connects with pgxpool (not database/sql) gets the same spans
// from cfg.ConnConfig.Tracer = dbtrace.NewPgxTracer().
func TestPgxTracerQueriesAreChildSpansWithSanitizedTextAndNoArgs(t *testing.T) {
	rec, tracer := recordSpans(t)
	pt := NewPgxTracer()

	ctx, parent := tracer.Start(context.Background(), "rpc")
	qctx := pt.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL:  "SELECT id FROM chats WHERE email = 'a@b.com' AND user_id = $1",
		Args: []any{"user-secret-arg"},
	})
	pt.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("SELECT 1")})
	parent.End()

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("got %d spans (%v), want the query and its parent", len(spans), names(spans))
	}
	q := spans[0]
	if q.Name() != "SELECT chats" {
		t.Errorf("span name = %q, want %q", q.Name(), "SELECT chats")
	}
	if q.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Errorf("the query span is not a child of the caller's span")
	}
	attrs := map[string]string{}
	for _, kv := range q.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
		if strings.Contains(kv.Value.Emit(), "secret") || strings.Contains(kv.Value.Emit(), "a@b.com") {
			t.Errorf("attribute %s leaks a literal or bind argument: %s", kv.Key, kv.Value.Emit())
		}
	}
	for k, want := range map[string]string{
		"db.system.name":     System,
		"db.operation.name":  "SELECT",
		"db.collection.name": "chats",
		"db.query.text":      "SELECT id FROM chats WHERE email = ? AND user_id = $1",
		"db.statement":       "SELECT id FROM chats WHERE email = ? AND user_id = $1",
	} {
		if attrs[k] != want {
			t.Errorf("%s = %q, want %q", k, attrs[k], want)
		}
	}
}

func TestPgxTracerRecordsErrors(t *testing.T) {
	rec, tracer := recordSpans(t)
	pt := NewPgxTracer()

	ctx, parent := tracer.Start(context.Background(), "rpc")
	qctx := pt.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "UPDATE chats SET title = $1"})
	pt.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{Err: errors.New("deadlock detected")})
	parent.End()

	q := rec.Ended()[0]
	if q.Status().Code != codes.Error {
		t.Errorf("status = %v, want Error", q.Status())
	}
}

// A query with no span in its context (a migration, a poller with no root
// span) is not a one-span trace of its own, and the caller's span is not
// ended by the tracer.
func TestPgxTracerOpensNoRootSpan(t *testing.T) {
	rec, tracer := recordSpans(t)
	pt := NewPgxTracer()

	qctx := pt.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	pt.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{})
	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("a query outside any span produced %d spans: %v", n, names(rec.Ended()))
	}

	ctx, parent := tracer.Start(context.Background(), "rpc")
	_ = pt.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	qctx = pt.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	pt.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{})
	for _, s := range rec.Ended() {
		if s.Name() == "rpc" {
			t.Fatalf("the tracer ended the caller's span")
		}
	}
	parent.End()
}

func TestPgxTracerBatchCopyAndConnect(t *testing.T) {
	rec, tracer := recordSpans(t)
	pt := NewPgxTracer()
	ctx, parent := tracer.Start(context.Background(), "rpc")

	bctx := pt.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{})
	pt.TraceBatchQuery(bctx, nil, pgx.TraceBatchQueryData{SQL: "INSERT INTO usage_events (a) VALUES ('x')"})
	pt.TraceBatchQuery(bctx, nil, pgx.TraceBatchQueryData{SQL: "DELETE FROM chats WHERE id = $1", Err: errors.New("boom")})
	pt.TraceBatchEnd(bctx, nil, pgx.TraceBatchEndData{})

	cctx := pt.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{TableName: pgx.Identifier{"public", "events"}})
	pt.TraceCopyFromEnd(cctx, nil, pgx.TraceCopyFromEndData{})

	kctx := pt.TraceConnectStart(ctx, pgx.TraceConnectStartData{})
	pt.TraceConnectEnd(kctx, pgx.TraceConnectEndData{})
	parent.End()

	got := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		got[s.Name()] = s
	}
	for _, name := range []string{"BATCH", "COPY public.events", "db.connect"} {
		if got[name] == nil {
			t.Fatalf("no %q span; got %v", name, names(rec.Ended()))
		}
	}
	var stmts []string
	for _, e := range got["BATCH"].Events() {
		if e.Name != "exception" {
			stmts = append(stmts, e.Name)
		}
	}
	if strings.Join(stmts, ",") != "INSERT usage_events,DELETE chats" {
		t.Fatalf("batch statement events = %v, want INSERT usage_events and DELETE chats", stmts)
	}
	if got["BATCH"].Status().Code != codes.Error {
		t.Errorf("a failed query in the batch left the batch span OK")
	}
}

// pgx's own interfaces: a tracer that stops satisfying them is silently not
// installed (cfg.ConnConfig.Tracer is an interface value).
var (
	_ pgx.QueryTracer    = (*PgxTracer)(nil)
	_ pgx.BatchTracer    = (*PgxTracer)(nil)
	_ pgx.CopyFromTracer = (*PgxTracer)(nil)
	_ pgx.ConnectTracer  = (*PgxTracer)(nil)
)
