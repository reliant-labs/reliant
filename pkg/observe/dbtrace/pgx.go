package dbtrace

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// PgxTracer traces a native pgx / pgxpool connection with the same spans
// OpenDB gives a database/sql pool: one client span per statement named
// "<OPERATION> <table>", db.query.text with every literal replaced by ?, bind
// arguments never recorded, and no span at all for work that has no caller
// span to hang under.
//
// Install it before the pool connects:
//
//	cfg, err := pgxpool.ParseConfig(dsn)
//	cfg.ConnConfig.Tracer = dbtrace.NewPgxTracer()
//	pool, err := pgxpool.NewWithConfig(ctx, cfg)
//
// It implements pgx.QueryTracer, BatchTracer, CopyFromTracer and
// ConnectTracer; a batch is one BATCH span with an event per statement.
// Prepare is not traced, for the reason OpenDB leaves it off.
type PgxTracer struct{}

// NewPgxTracer returns the tracer to assign to a pgx ConnConfig.Tracer.
func NewPgxTracer() *PgxTracer { return &PgxTracer{} }

const pgxTracerName = "github.com/reliant-labs/forge/pkg/observe/dbtrace"

// pgxSpanKey carries the span a Trace*Start opened to its Trace*End. The
// context's own span is not that span when none was opened (no caller span),
// and ending it would end the caller's.
type pgxSpanKey struct{}

func (*PgxTracer) start(ctx context.Context, name string, attrs ...attribute.KeyValue) context.Context {
	parent := trace.SpanFromContext(ctx)
	if !parent.SpanContext().IsValid() {
		return ctx
	}
	ctx, span := parent.TracerProvider().Tracer(pgxTracerName).Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append([]attribute.KeyValue{
			attribute.String("db.system.name", System),
			attribute.String("db.system", System),
		}, attrs...)...))
	return context.WithValue(ctx, pgxSpanKey{}, span)
}

func ownSpan(ctx context.Context) trace.Span {
	span, _ := ctx.Value(pgxSpanKey{}).(trace.Span)
	return span
}

func endSpan(ctx context.Context, err error) {
	span := ownSpan(ctx)
	if span == nil {
		return
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func statementName(query string) string {
	op, table := Summarize(query)
	switch {
	case op == "":
		return "query"
	case table == "":
		return op
	default:
		return op + " " + table
	}
}

// statementAttributes is attributes() for a statement outside database/sql;
// it never reads bind arguments.
func statementAttributes(query string) []attribute.KeyValue {
	if query == "" {
		return nil
	}
	op, table := Summarize(query)
	text := SanitizeSQL(query)
	attrs := []attribute.KeyValue{
		attribute.String("db.query.text", text),
		attribute.String("db.statement", text),
	}
	if op != "" {
		attrs = append(attrs, attribute.String("db.operation.name", op))
	}
	if table != "" {
		attrs = append(attrs, attribute.String("db.collection.name", table))
	}
	return attrs
}

// TraceQueryStart implements pgx.QueryTracer.
func (t *PgxTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return t.start(ctx, statementName(data.SQL), statementAttributes(data.SQL)...)
}

// TraceQueryEnd implements pgx.QueryTracer.
func (*PgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	endSpan(ctx, data.Err)
}

// TraceBatchStart implements pgx.BatchTracer.
func (t *PgxTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return t.start(ctx, "BATCH")
}

// TraceBatchQuery implements pgx.BatchTracer. Each statement is an event on
// the batch span, carrying the same sanitized text a query span would.
func (*PgxTracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	span := ownSpan(ctx)
	if span == nil {
		return
	}
	span.AddEvent(statementName(data.SQL), trace.WithAttributes(statementAttributes(data.SQL)...))
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
}

// TraceBatchEnd implements pgx.BatchTracer.
func (*PgxTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	endSpan(ctx, data.Err)
}

// TraceCopyFromStart implements pgx.CopyFromTracer.
func (t *PgxTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	table := strings.Join(data.TableName, ".")
	return t.start(ctx, strings.TrimSpace("COPY "+table),
		attribute.String("db.operation.name", "COPY"),
		attribute.String("db.collection.name", table))
}

// TraceCopyFromEnd implements pgx.CopyFromTracer.
func (*PgxTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	endSpan(ctx, data.Err)
}

// TraceConnectStart implements pgx.ConnectTracer.
func (t *PgxTracer) TraceConnectStart(ctx context.Context, _ pgx.TraceConnectStartData) context.Context {
	return t.start(ctx, "db.connect")
}

// TraceConnectEnd implements pgx.ConnectTracer.
func (*PgxTracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	endSpan(ctx, data.Err)
}
