// Copyright (c) 2025 Reliant Labs

package chquery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/reliant-labs/forge/pkg/alerting"
)

type neverConn struct{ called bool }

func (n *neverConn) Query(context.Context, string, ...any) (driver.Rows, error) {
	n.called = true
	return nil, errors.New("should not be reached")
}

func TestQuerierRefusesBeforeTouchingTheConnection(t *testing.T) {
	c := &neverConn{}
	_, err := New(c).Query(context.Background(), "any-owner", "DROP TABLE x", alerting.QueryOptions{})
	if !errors.Is(err, alerting.ErrBadExpr) {
		t.Fatalf("err = %v", err)
	}
	if c.called {
		t.Fatal("a refused statement reached the database")
	}
}

// fakeRows plays back a result set through driver.Rows: column names and
// scan types, then each row's values written into the pointers Scan is given.
type fakeRows struct {
	names   []string
	types   []reflect.Type
	data    [][]any
	pos     int
	scanErr error
	iterErr error
	closed  bool
}

type colType struct {
	name string
	typ  reflect.Type
}

func (c colType) Name() string             { return c.name }
func (c colType) Nullable() bool           { return false }
func (c colType) ScanType() reflect.Type   { return c.typ }
func (c colType) DatabaseTypeName() string { return c.typ.String() }

func (r *fakeRows) Columns() []string { return r.names }
func (r *fakeRows) ColumnTypes() []driver.ColumnType {
	out := make([]driver.ColumnType, len(r.names))
	for i := range out {
		out[i] = colType{r.names[i], r.types[i]}
	}
	return out
}
func (r *fakeRows) Next() bool {
	r.pos++
	return r.pos <= len(r.data)
}
func (r *fakeRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	for i, v := range r.data[r.pos-1] {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}
func (r *fakeRows) ScanStruct(any) error { return errors.New("unused") }
func (r *fakeRows) Totals(...any) error  { return errors.New("unused") }
func (r *fakeRows) Close() error         { r.closed = true; return nil }
func (r *fakeRows) Err() error           { return r.iterErr }
func (r *fakeRows) HasData() bool        { return len(r.data) > 0 }

// rowsConn answers any statement with rows (or err), recording what it was
// asked.
type rowsConn struct {
	rows  *fakeRows
	err   error
	query string
	calls int
}

func (c *rowsConn) Query(_ context.Context, query string, _ ...any) (driver.Rows, error) {
	c.calls++
	c.query = query
	if c.err != nil {
		return nil, c.err
	}
	return c.rows, nil
}

func resultSet() *fakeRows {
	return &fakeRows{
		names: []string{"service", "errors", "ratio"},
		types: []reflect.Type{reflect.TypeFor[string](), reflect.TypeFor[uint64](), reflect.TypeFor[float64]()},
		data: [][]any{
			{"api", uint64(7), 0.25},
			{"worker", uint64(0), 0.0},
		},
	}
}

// Scan reads every row into a Row keyed by column name, each value in its
// column's own Go type, and an empty result is no rows at all.
func TestScan_ReadsEveryRowByColumnName(t *testing.T) {
	got, err := Scan(resultSet())
	if err != nil {
		t.Fatal(err)
	}
	want := []alerting.Row{
		{"service": "api", "errors": uint64(7), "ratio": 0.25},
		{"service": "worker", "errors": uint64(0), "ratio": 0.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %#v\nwant %#v", got, want)
	}

	empty := resultSet()
	empty.data = nil
	if got, err := Scan(empty); err != nil || len(got) != 0 {
		t.Fatalf("an empty result = %v, %v; want no rows", got, err)
	}
}

func TestScan_ReportsScanAndIterationFailures(t *testing.T) {
	boom := errors.New("bad block")
	bad := resultSet()
	bad.scanErr = boom
	if _, err := Scan(bad); !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "chquery: scanning: ") {
		t.Fatalf("a scan failure = %v, want it wrapped as chquery: scanning", err)
	}

	broken := resultSet()
	broken.iterErr = errors.New("connection lost mid-stream")
	if _, err := Scan(broken); !errors.Is(err, broken.iterErr) {
		t.Fatalf("an iteration failure = %v, want rows.Err()", err)
	}
}

// Querier.Query ignores the owner, hands the guarded statement to the
// connection, returns its rows and closes the result.
func TestQuerier_QueryRunsTheGuardedStatement(t *testing.T) {
	conn := &rowsConn{rows: resultSet()}
	rows, err := New(conn).Query(context.Background(), "ignored-owner",
		"SELECT service, errors, ratio FROM logs", alerting.QueryOptions{MaxResultRows: 100, MaxExecutionTime: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["service"] != "api" {
		t.Fatalf("rows = %v", rows)
	}
	if conn.query != "SELECT service, errors, ratio FROM logs" || conn.calls != 1 {
		t.Fatalf("the connection saw %q (%d calls)", conn.query, conn.calls)
	}
	if !conn.rows.closed {
		t.Fatal("the result set was not closed")
	}
}

// A query the connection fails is returned untouched, and a result is closed
// even when reading it fails.
func TestRun_ConnectionAndReadFailures(t *testing.T) {
	boom := errors.New("code: 159, Timeout exceeded")
	if _, err := Run(context.Background(), &rowsConn{err: boom}, "SELECT 1", alerting.QueryOptions{}); !errors.Is(err, boom) {
		t.Fatalf("a failing connection = %v, want its error", err)
	}

	conn := &rowsConn{rows: resultSet()}
	conn.rows.scanErr = errors.New("decode")
	if _, err := Run(context.Background(), conn, "SELECT 1", alerting.QueryOptions{}); err == nil {
		t.Fatal("a failing scan was swallowed")
	}
	if !conn.rows.closed {
		t.Fatal("the result set leaked when reading it failed")
	}
}

// The guard refuses each of these before the connection is touched.
func TestRun_RefusesUnsafeStatementsBeforeTheConnection(t *testing.T) {
	for name, q := range map[string]string{
		"a write":          "INSERT INTO t VALUES (1)",
		"DDL":              "DROP TABLE t",
		"two statements":   "SELECT 1; SELECT 2",
		"empty":            "",
		"only whitespace":  "   \n",
		"not a select":     "SHOW TABLES",
		"an oversize body": "SELECT '" + strings.Repeat("x", alerting.MaxExprBytes) + "'",
	} {
		t.Run(name, func(t *testing.T) {
			conn := &rowsConn{rows: resultSet()}
			_, err := Run(context.Background(), conn, q, alerting.QueryOptions{})
			if !errors.Is(err, alerting.ErrBadExpr) {
				t.Fatalf("err = %v, want ErrBadExpr", err)
			}
			if conn.calls != 0 {
				t.Fatal("a refused statement reached the connection")
			}
		})
	}
	// A WITH-prefixed read and a trailing semicolon are accepted.
	for _, q := range []string{"WITH x AS (SELECT 1 AS n) SELECT n FROM x", "SELECT 1;"} {
		conn := &rowsConn{rows: resultSet()}
		if _, err := Run(context.Background(), conn, q, alerting.QueryOptions{}); err != nil {
			t.Errorf("%q refused: %v", q, err)
		}
	}
}
