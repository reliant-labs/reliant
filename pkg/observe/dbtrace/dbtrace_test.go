package dbtrace

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// recordSpans installs a span recorder as the global tracer provider (otelsql
// reads it when the pool is opened) and restores the previous one.
func recordSpans(t *testing.T) (*tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec, tp.Tracer("test")
}

func TestQueriesAreChildSpansWithSanitizedTextAndNoArgs(t *testing.T) {
	rec, tracer := recordSpans(t)
	db := OpenDB(fakeConnector{})
	defer db.Close()

	ctx, parent := tracer.Start(context.Background(), "rpc")
	rows, err := db.QueryContext(ctx, "SELECT id FROM chats WHERE email = 'a@b.com' AND user_id = $1", "user-secret-arg")
	if err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if _, err := db.ExecContext(ctx, "UPDATE chats SET title = $1", "title-secret-arg"); err != nil {
		t.Fatal(err)
	}
	parent.End()

	var queries []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "rpc" || s.Name() == "db.connect" {
			continue
		}
		queries = append(queries, s)
	}
	if len(queries) != 2 {
		t.Fatalf("got %d query spans (%v), want 2", len(queries), names(rec.Ended()))
	}
	sel, upd := queries[0], queries[1]
	if sel.Name() != "SELECT chats" || upd.Name() != "UPDATE chats" {
		t.Fatalf("span names = %q, %q; want \"SELECT chats\", \"UPDATE chats\"", sel.Name(), upd.Name())
	}
	for _, s := range queries {
		if s.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Errorf("%s: parent = %s, want the rpc span", s.Name(), s.Parent().SpanID())
		}
		if s.SpanKind() != trace.SpanKindClient {
			t.Errorf("%s: kind = %v, want client", s.Name(), s.SpanKind())
		}
		for _, kv := range s.Attributes() {
			if strings.Contains(kv.Value.Emit(), "secret") || strings.Contains(kv.Value.Emit(), "a@b.com") {
				t.Errorf("%s: attribute %s leaks data: %q", s.Name(), kv.Key, kv.Value.Emit())
			}
		}
	}
	want := map[attribute.Key]string{
		"db.query.text":      "SELECT id FROM chats WHERE email = ? AND user_id = $1",
		"db.operation.name":  "SELECT",
		"db.collection.name": "chats",
		"db.system.name":     "postgresql",
	}
	got := map[attribute.Key]string{}
	for _, kv := range sel.Attributes() {
		got[kv.Key] = kv.Value.Emit()
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("SELECT span %s = %q, want %q", k, got[k], v)
		}
	}
}

// A query with no span in its context opens none: it would otherwise be a
// one-span trace of its own.
func TestQueryWithoutAParentSpanOpensNone(t *testing.T) {
	rec, _ := recordSpans(t)
	db := OpenDB(fakeConnector{})
	defer db.Close()

	if _, err := db.ExecContext(context.Background(), "DELETE FROM tokens"); err != nil {
		t.Fatal(err)
	}
	if got := rec.Ended(); len(got) != 0 {
		t.Fatalf("spans = %v, want none", names(got))
	}
}

func TestTransactionSpans(t *testing.T) {
	rec, tracer := recordSpans(t)
	db := OpenDB(fakeConnector{})
	defer db.Close()

	ctx, parent := tracer.Start(context.Background(), "rpc")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO usage_events (a) VALUES ($1)", 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	parent.End()

	got := strings.Join(names(rec.Ended()), ",")
	for _, want := range []string{"BEGIN", "INSERT usage_events", "COMMIT"} {
		if !strings.Contains(got, want) {
			t.Errorf("spans %s: missing %s", got, want)
		}
	}
}

func TestIsTraced(t *testing.T) {
	if !IsTraced(OpenDB(fakeConnector{})) {
		t.Error("IsTraced(OpenDB(...)) = false")
	}
	if IsTraced(sql.OpenDB(fakeConnector{})) {
		t.Error("IsTraced(sql.OpenDB(...)) = true for an untraced pool")
	}
	if IsTraced(nil) {
		t.Error("IsTraced(nil) = true")
	}
}

// registerFake registers the fake driver once per test binary: sql.Register
// panics on a second registration, which -count=N would otherwise make.
var registerFake = sync.OnceFunc(func() { sql.Register("dbtrace-fake", fakeDriver{}) })

func TestOpenByDriverName(t *testing.T) {
	registerFake()
	db, err := Open("dbtrace-fake", "dsn")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !IsTraced(db) {
		t.Error("Open did not trace the pool")
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
}

func names(spans []sdktrace.ReadOnlySpan) []string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = s.Name()
	}
	return out
}

// fake driver: every statement succeeds and returns no rows.

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{}, nil }

type fakeConnector struct{}

func (fakeConnector) Connect(context.Context) (driver.Conn, error) { return &fakeConn{}, nil }
func (fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeConn struct{ closed atomic.Bool }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *fakeConn) Close() error                        { c.closed.Store(true); return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return fakeTx{}, nil }
func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return fakeTx{}, nil
}
func (c *fakeConn) Ping(context.Context) error { return nil }
func (c *fakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (c *fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return fakeRows{}, nil
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeRows struct{}

func (fakeRows) Columns() []string         { return []string{"id"} }
func (fakeRows) Close() error              { return nil }
func (fakeRows) Next([]driver.Value) error { return io.EOF }
