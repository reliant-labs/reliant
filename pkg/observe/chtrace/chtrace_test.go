package chtrace

import (
	"context"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// fakeConn answers every statement with err, and Query with rows of n rows.
type fakeConn struct {
	driver.Conn
	err  error
	rows int
	// rowsErr is what the rows report after the last one.
	rowsErr error
	// scanErr is what QueryRow's row reports from Scan and ScanStruct.
	scanErr error
	got     []string
}

func (f *fakeConn) Query(_ context.Context, q string, _ ...any) (driver.Rows, error) {
	f.got = append(f.got, q)
	if f.err != nil {
		return nil, f.err
	}
	return &fakeRows{left: f.rows, err: f.rowsErr, scanErr: f.scanErr}, nil
}

func (f *fakeConn) Exec(_ context.Context, q string, _ ...any) error {
	f.got = append(f.got, q)
	return f.err
}

func (f *fakeConn) Select(_ context.Context, _ any, q string, _ ...any) error {
	f.got = append(f.got, q)
	return f.err
}

func (f *fakeConn) QueryRow(_ context.Context, q string, _ ...any) driver.Row {
	f.got = append(f.got, q)
	return fakeRow{err: f.err, scanErr: f.scanErr}
}

type fakeRows struct {
	driver.Rows
	left         int
	err, scanErr error
}

func (r *fakeRows) Scan(...any) error    { return r.scanErr }
func (r *fakeRows) ScanStruct(any) error { return r.scanErr }
func (r *fakeRows) Totals(...any) error  { return r.scanErr }

func (r *fakeRows) Next() bool {
	if r.left == 0 {
		return false
	}
	r.left--
	return true
}
func (r *fakeRows) Close() error { return nil }
func (r *fakeRows) Err() error   { return r.err }

type fakeRow struct {
	driver.Row
	err, scanErr error
}

func (r fakeRow) Err() error           { return r.err }
func (r fakeRow) Scan(...any) error    { return r.scanErr }
func (r fakeRow) ScanStruct(any) error { return r.scanErr }

func underSpan(t *testing.T) (context.Context, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, _ := tp.Tracer("test").Start(context.Background(), "handler")
	return ctx, rec
}

func attrs(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	m := map[attribute.Key]attribute.Value{}
	for _, a := range s.Attributes() {
		m[a.Key] = a.Value
	}
	return m
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// A Query is one CLIENT span named by its summary, open until the rows are
// closed, carrying the sanitized text, the query id and the rows returned.
// The EXECUTE AS prefix is not the operation.
func TestQueryIsASpanUntilTheRowsClose(t *testing.T) {
	ctx, rec := underSpan(t)
	fc := &fakeConn{rows: 3}
	conn := Wrap(fc)
	rows, err := conn.Query(ctx, "EXECUTE AS `r_org_a` SELECT ServiceName FROM t_org_a.otel_logs WHERE Body = 'secret' AND x = {p1:String}", "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Ended()) != 0 {
		t.Fatal("the span ended before the rows were read")
	}
	for rows.Next() {
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("%d spans, want 1", len(ended))
	}
	s := ended[0]
	if s.Name() != "SELECT t_org_a.otel_logs" || s.SpanKind() != trace.SpanKindClient {
		t.Errorf("span = %q %v", s.Name(), s.SpanKind())
	}
	a := attrs(s)
	for k, want := range map[attribute.Key]string{
		"db.system.name":     "clickhouse",
		"db.system":          "clickhouse",
		"db.operation.name":  "SELECT",
		"db.collection.name": "t_org_a.otel_logs",
		"db.query.summary":   "SELECT t_org_a.otel_logs",
	} {
		if got := a[k].AsString(); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if text := a["db.query.text"].AsString(); text == "" || regexp.MustCompile(`secret`).MatchString(text) {
		t.Errorf("db.query.text = %q: want the statement with its literals removed", text)
	}
	if id := a[AttrQueryID].AsString(); !uuidRe.MatchString(id) {
		t.Errorf("query id = %q, want a UUID", id)
	}
	if got := a["db.response.returned_rows"].AsInt64(); got != 3 {
		t.Errorf("returned rows = %d, want 3", got)
	}
	for _, k := range []attribute.Key{AttrReadRows, AttrReadBytes} {
		if _, ok := a[k]; !ok {
			t.Errorf("%s is missing", k)
		}
	}
	if s.Status().Code == codes.Error {
		t.Errorf("a successful statement is marked failed: %v", s.Status())
	}
	if fc.got[0][:10] != "EXECUTE AS" {
		t.Errorf("the statement reached the server changed: %q", fc.got[0])
	}
}

// A failure is the span's status and message, an exception event, and the
// ClickHouse exception's name and code; a cancellation says so.
func TestFailuresAreRecordedOnTheSpan(t *testing.T) {
	ctx, rec := underSpan(t)
	timeout := &clickhouse.Exception{Code: 159, Name: "TIMEOUT_EXCEEDED", Message: "Timeout exceeded: elapsed 20 seconds"}
	_, _ = Wrap(&fakeConn{err: timeout}).Query(ctx, "SELECT 1")
	_ = Wrap(&fakeConn{err: context.Canceled}).Exec(ctx, "INSERT INTO t VALUES (1)")
	rows, _ := Wrap(&fakeConn{rows: 1, rowsErr: context.DeadlineExceeded}).Query(ctx, "SELECT a FROM t")
	for rows.Next() {
	}
	_ = rows.Close()
	ended := rec.Ended()
	if len(ended) != 3 {
		t.Fatalf("%d spans, want 3", len(ended))
	}
	for i, want := range []struct{ errType, code, msg string }{
		{"TIMEOUT_EXCEEDED", "159", "Timeout exceeded"},
		{"context_canceled", "", "context canceled"},
		{"context_deadline_exceeded", "", "context deadline exceeded"},
	} {
		s := ended[i]
		if s.Status().Code != codes.Error || !regexp.MustCompile(want.msg).MatchString(s.Status().Description) {
			t.Errorf("%s: status = %v, want Error with %q", s.Name(), s.Status(), want.msg)
		}
		a := attrs(s)
		if got := a["error.type"].AsString(); got != want.errType {
			t.Errorf("%s: error.type = %q, want %q", s.Name(), got, want.errType)
		}
		if got := a["db.response.status_code"].AsString(); got != want.code {
			t.Errorf("%s: status code = %q, want %q", s.Name(), got, want.code)
		}
		var exception bool
		for _, e := range s.Events() {
			exception = exception || e.Name == "exception"
		}
		if !exception {
			t.Errorf("%s: no exception event", s.Name())
		}
	}
}

// expiringConn is a driver that, like clickhouse-go under load, waits for the
// statement's context to end and then returns its own error rather than
// ctx.Err(): the socket read that timed out, or the connection the cancel broke.
type expiringConn struct {
	driver.Conn
	err error
}

func (c expiringConn) Exec(ctx context.Context, _ string, _ ...any) error {
	<-ctx.Done()
	return c.err
}

func (c expiringConn) Query(ctx context.Context, _ string, _ ...any) (driver.Rows, error) {
	<-ctx.Done()
	return nil, c.err
}

func (c expiringConn) QueryRow(ctx context.Context, _ string, _ ...any) driver.Row {
	<-ctx.Done()
	return fakeRow{scanErr: c.err}
}

// pastDeadline is a context whose deadline has passed but whose timer has not
// yet marked it done: the instant clickhouse-go's socket deadline (set to
// ctx's) fires first, so the read fails while ctx.Err() is still nil.
type pastDeadline struct{ context.Context }

func (pastDeadline) Deadline() (time.Time, bool) { return time.Now().Add(-time.Millisecond), true }

// clickhouse-go's text around a socket read that hit ctx's deadline.
func readTimeout() error {
	return fmt.Errorf("query processing: failed to read packet from 127.0.0.1:9000 (conn_id=1): %w",
		&net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded})
}

// A statement that ran out of its context's time is context_deadline_exceeded
// however the driver spelled the failure — an i/o timeout, a broken
// connection, an opaque string — and its status says the deadline ended it.
// A network timeout with the context still live is the driver's own: timeout.
func TestAnExpiredStatementIsItsContextsVerdict(t *testing.T) {
	parent, rec := underSpan(t)
	for _, tc := range []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		err     error
		errType string
		status  string
	}{
		{"opaque error after the deadline", deadline(parent), sqldriver.ErrBadConn, "context_deadline_exceeded", "deadline exceeded"},
		{"read timeout after the deadline", deadline(parent), readTimeout(), "context_deadline_exceeded", "deadline exceeded"},
		{"read timeout before ctx's timer fired", func() (context.Context, context.CancelFunc) {
			return pastDeadline{parent}, func() {} // never done: ctx.Err() stays nil
		}, readTimeout(), "context_deadline_exceeded", "deadline exceeded"},
		{"opaque error after a cancel", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(parent)
			cancel()
			return ctx, cancel
		}, errors.New("EOF"), "context_canceled", "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(rec.Ended())
			ctx, cancel := tc.ctx()
			defer cancel()
			conn := Wrap(expiringConn{err: tc.err})
			if ctx.Done() == nil {
				conn = Wrap(&fakeConn{err: tc.err}) // never done: the driver returns at once
			}
			_ = conn.Exec(ctx, "SELECT sleepEachRow(1) FROM numbers(5)")
			_, _ = conn.Query(ctx, "SELECT sleepEachRow(1) FROM numbers(5)")
			_ = conn.QueryRow(ctx, "SELECT sum(sleepEachRow(1)) FROM numbers(5)").Scan(new(uint64))
			ended := rec.Ended()[before:]
			if len(ended) != 3 {
				t.Fatalf("%d spans, want 3", len(ended))
			}
			for _, s := range ended {
				if got := attrs(s)["error.type"].AsString(); got != tc.errType {
					t.Errorf("%s: error.type = %q, want %q", s.Name(), got, tc.errType)
				}
				if d := s.Status().Description; s.Status().Code != codes.Error ||
					!strings.Contains(d, tc.err.Error()) || !strings.Contains(d, tc.status) {
					t.Errorf("%s: status = %v, want Error with the driver's message and %q", s.Name(), s.Status(), tc.status)
				}
			}
		})
	}

	// The context is still live: the driver's own read timeout is not the
	// caller's deadline, and a ClickHouse exception is the server's verdict
	// even when the context expired by the time the rows were closed.
	if got := ErrorTypeContext(parent, readTimeout()); got != "timeout" {
		t.Errorf("a read timeout under a live context = %q, want timeout", got)
	}
	expired, cancel := context.WithCancel(parent)
	cancel()
	mem := &clickhouse.Exception{Code: 241, Name: "MEMORY_LIMIT_EXCEEDED"}
	if got := ErrorTypeContext(expired, fmt.Errorf("read: %w", mem)); got != "MEMORY_LIMIT_EXCEEDED" {
		t.Errorf("an exception under an expired context = %q, want the exception's name", got)
	}
}

// deadline returns a ctx factory: parent with a deadline that has passed.
func deadline(parent context.Context) func() (context.Context, context.CancelFunc) {
	return func() (context.Context, context.CancelFunc) {
		return context.WithDeadline(parent, time.Now().Add(-time.Second))
	}
}

func TestSelectQueryRowAndExecAreSpans(t *testing.T) {
	ctx, rec := underSpan(t)
	conn := Wrap(&fakeConn{})
	_ = conn.Select(ctx, &[]int{}, "SELECT n FROM numbers")
	_ = conn.QueryRow(ctx, "SELECT count() FROM t").Scan(new(uint64))
	_ = conn.Exec(ctx, "ALTER TABLE t DELETE WHERE 1")
	var names []string
	for _, s := range rec.Ended() {
		names = append(names, s.Name())
	}
	if len(names) != 3 || names[0] != "SELECT numbers" || names[1] != "SELECT t" || names[2] != "ALTER" {
		t.Errorf("spans = %v", names)
	}
}

// No caller span, no span: a statement outside a request is not a trace.
func TestNoSpanWithoutACallerSpan(t *testing.T) {
	_, rec := underSpan(t)
	fc := &fakeConn{rows: 1}
	rows, err := Wrap(fc).Query(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rows.(*tracedRows); ok {
		t.Error("rows were wrapped with no span to end")
	}
	_ = rows.Close()
	if n := len(rec.Ended()); n != 0 {
		t.Errorf("%d spans with no caller span", n)
	}
	if Wrap(Wrap(fc)).(*tracedConn).Conn != fc {
		t.Error("wrapping twice nests the tracer")
	}
}

func TestStripExecuteAs(t *testing.T) {
	for in, want := range map[string]string{
		"EXECUTE AS `r_a` SELECT 1":       "SELECT 1",
		"execute as r_a\nSELECT 1":        "SELECT 1",
		"  EXECUTE AS \"u x\" INSERT ...": "INSERT ...",
		"SELECT 1":                        "SELECT 1",
		"EXECUTE AS `unterminated":        "EXECUTE AS `unterminated",
	} {
		if got := stripExecuteAs(in); got != want {
			t.Errorf("stripExecuteAs(%q) = %q, want %q", in, got, want)
		}
	}
	if ErrorType(errors.New("x")) != "_OTHER" {
		t.Error("an unclassified error is not _OTHER")
	}
}

// A QueryRow span lasts until the row is scanned, and a scan that fails is
// the statement's failure; a statement that failed outright ends at once.
func TestQueryRowSpanEndsAtScan(t *testing.T) {
	ctx, rec := underSpan(t)
	row := Wrap(&fakeConn{}).QueryRow(ctx, "SELECT count() FROM t")
	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("the span ended before the row was scanned (%d ended)", n)
	}
	if err := row.Scan(new(uint64)); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.Ended()); n != 1 {
		t.Fatalf("%d spans after Scan, want 1", n)
	}
	if s := rec.Ended()[0]; s.Status().Code == codes.Error {
		t.Errorf("a successful scan is marked failed: %v", s.Status())
	}

	bad := errors.New("converting UInt64 to *string is unsupported")
	_ = Wrap(&fakeConn{scanErr: bad}).QueryRow(ctx, "SELECT count() FROM t").ScanStruct(&struct{}{})
	if s := rec.Ended()[1]; s.Status().Code != codes.Error || s.Status().Description != bad.Error() {
		t.Errorf("a failed ScanStruct: status = %v, want Error %q", s.Status(), bad)
	}

	timeout := &clickhouse.Exception{Code: 159, Name: "TIMEOUT_EXCEEDED", Message: "Timeout exceeded"}
	row = Wrap(&fakeConn{err: timeout}).QueryRow(ctx, "SELECT count() FROM t")
	if n := len(rec.Ended()); n != 3 {
		t.Fatalf("a QueryRow that failed outright left its span open (%d ended)", n)
	}
	if err := row.Scan(new(uint64)); err != nil {
		t.Fatal(err) // the fake's row scans nothing; the point is no second end
	}
	if n := len(rec.Ended()); n != 3 {
		t.Errorf("a failed QueryRow's span ended twice: %d ended", n)
	}
}

// A ClickHouse literal whose quote is backslash-escaped stays out of both
// recorded texts: read by Postgres's rules it would end at the escape.
func TestEscapedLiteralsNeverRecorded(t *testing.T) {
	_, attrs := Describe(`SELECT a FROM t WHERE b = 'x\'secret' AND c = ‘secret’ -- secret`)
	for _, a := range attrs {
		if strings.Contains(a.Value.Emit(), "secret") {
			t.Errorf("%s = %q carries the literal", a.Key, a.Value.Emit())
		}
	}
}

// A row that fails to scan fails the Query's span, though the rows report no
// error and close cleanly: conversion errors never reach Rows.Err.
func TestQueryRowsScanFailureFailsTheSpan(t *testing.T) {
	ctx, rec := underSpan(t)
	bad := errors.New("converting String to *int64 is unsupported")
	for i, scan := range []func(driver.Rows) error{
		func(r driver.Rows) error { return r.Scan(new(int64)) },
		func(r driver.Rows) error { return r.ScanStruct(&struct{}{}) },
		func(r driver.Rows) error { return r.Totals(new(int64)) },
	} {
		rows, err := Wrap(&fakeConn{rows: 2, scanErr: bad}).Query(ctx, "SELECT a FROM t")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			if err := scan(rows); !errors.Is(err, bad) {
				t.Fatalf("scan %d returned %v, want the conversion error", i, err)
			}
		}
		if rows.Err() != nil || rows.Close() != nil {
			t.Fatal("the fake rows must report no error of their own")
		}
		s := rec.Ended()[i]
		if s.Status().Code != codes.Error || s.Status().Description != bad.Error() {
			t.Errorf("scan %d: status = %v, want Error %q", i, s.Status(), bad)
		}
	}
}
