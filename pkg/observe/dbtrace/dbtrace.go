// Package dbtrace opens a *sql.DB whose queries are OpenTelemetry spans.
//
// Every query a forge process runs goes through database/sql: the ORM (bun)
// sits on a *sql.DB, and hand-written SQL calls it directly. Tracing at the
// driver is therefore the one place that sees all of it; an ORM hook sees
// only ORM traffic, which in practice is the minority. serverkit.OpenDB opens
// its pool through this package, so a server gets query spans with no code of
// its own.
//
// What a span carries, and what it never does:
//
//   - One CLIENT span per statement (Exec/Query on a connection or prepared
//     statement), named "<OPERATION> <table>" — "SELECT chats", "INSERT
//     usage_events" — plus BEGIN/COMMIT/ROLLBACK and a db.connect span when the
//     pool dials. Ping, ResetSession, Prepare and the per-row spans are off:
//     they multiply the span count without saying where the time went.
//   - db.query.text is the statement with every literal replaced by ? (see
//     SanitizeSQL). Bind arguments are never recorded: they are user data.
//   - A span is only opened under an existing span. A query run with no span
//     in its context — a startup migration, a poller with no root span — would
//     otherwise become a one-span trace of its own, which is noise, not depth.
//     Give the loop a root span (lifecyclekit does) and its queries appear
//     under it.
//
// A native pgx / pgxpool connection (no database/sql) gets the same spans from
// NewPgxTracer, assigned to cfg.ConnConfig.Tracer.
//
// Metrics are not emitted here: the pool metrics come from
// RegisterDBStatsMetrics-style registration elsewhere, and the ORM's query
// histogram (go.sql.query_timing) from pkg/orm.
package dbtrace

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"

	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
)

// System is the db.system.name every span carries. Forge is postgres-pinned.
const System = "postgresql"

// OpenDB is sql.OpenDB with every statement traced. The connector is wrapped,
// not replaced, so a connector with its own behaviour (serverkit's
// pooler-drain retry) keeps it.
func OpenDB(c driver.Connector) *sql.DB {
	return otelsql.OpenDB(c, options()...)
}

// Open is sql.Open with every statement traced. Like sql.Open it does not
// connect; the first statement (or a Ping) does.
func Open(driverName, dsn string) (*sql.DB, error) {
	probe, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	d := probe.Driver()
	_ = probe.Close()
	if dc, ok := d.(driver.DriverContext); ok {
		c, err := dc.OpenConnector(dsn)
		if err != nil {
			return nil, err
		}
		return OpenDB(c), nil
	}
	return OpenDB(dsnConnector{dsn: dsn, driver: d}), nil
}

// IsTraced reports whether db's statements are already traced at the driver —
// opened by this package, or wrapped with otelsql by the application itself.
// pkg/orm asks, so an ORM on a traced pool does not open a second span per
// query.
func IsTraced(db *sql.DB) bool {
	if db == nil {
		return false
	}
	t := reflect.TypeOf(db.Driver())
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t != nil && t.PkgPath() == otelsqlPkgPath
}

// otelsqlPkgPath is where the driver wrapper otelsql puts on every pool it
// opens is declared. The type itself is unexported, so its package is the
// only thing to recognise it by.
const otelsqlPkgPath = "github.com/XSAM/otelsql"

func options() []otelsql.Option {
	return []otelsql.Option{
		otelsql.WithSpanOptions(otelsql.SpanOptions{
			DisableErrSkip:       true,
			DisableQuery:         true, // db.query.text is set sanitized, by attributes
			OmitConnResetSession: true,
			OmitConnPrepare:      true,
			OmitRows:             true,
			SpanFilter:           underASpan,
		}),
		otelsql.WithSpanNameFormatter(spanName),
		otelsql.WithAttributesGetter(attributes),
		otelsql.WithAttributes(attribute.String("db.system.name", System), attribute.String("db.system", System)),
		otelsql.WithMeterProvider(noop.NewMeterProvider()),
	}
}

func underASpan(ctx context.Context, _ otelsql.Method, _ string, _ []driver.NamedValue) bool {
	return trace.SpanContextFromContext(ctx).IsValid()
}

func spanName(_ context.Context, method otelsql.Method, query string) string {
	switch method {
	case otelsql.MethodConnBeginTx:
		return "BEGIN"
	case otelsql.MethodTxCommit:
		return "COMMIT"
	case otelsql.MethodTxRollback:
		return "ROLLBACK"
	case otelsql.MethodConnectorConnect:
		return "db.connect"
	}
	op, table := Summarize(query)
	switch {
	case op == "":
		return string(method)
	case table == "":
		return op
	default:
		return op + " " + table
	}
}

// attributes never reads args: bind values are user data.
func attributes(_ context.Context, _ otelsql.Method, query string, _ []driver.NamedValue) []attribute.KeyValue {
	if query == "" {
		return nil
	}
	op, table := Summarize(query)
	text := SanitizeSQL(query)
	attrs := make([]attribute.KeyValue, 0, 4)
	// db.statement is the pre-1.26 name of db.query.text; trace UIs still
	// read it for their database views.
	attrs = append(attrs, attribute.String("db.query.text", text), attribute.String("db.statement", text))
	if op != "" {
		attrs = append(attrs, attribute.String("db.operation.name", op))
	}
	if table != "" {
		attrs = append(attrs, attribute.String("db.collection.name", table))
	}
	return attrs
}

// dsnConnector is database/sql's own fallback for a driver without
// DriverContext.
type dsnConnector struct {
	dsn    string
	driver driver.Driver
}

func (c dsnConnector) Connect(context.Context) (driver.Conn, error) { return c.driver.Open(c.dsn) }
func (c dsnConnector) Driver() driver.Driver                        { return c.driver }
