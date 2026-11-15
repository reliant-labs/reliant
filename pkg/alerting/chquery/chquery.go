// Copyright (c) 2025 Reliant Labs

// Package chquery is a single-owner alerting.Querier over one ClickHouse
// connection: the owner key is ignored and every rule runs against the
// connection's database. It is what a self-hoster runs, and the reference for a
// multi-owner Querier, which differs only in choosing the connection (or the
// identity it runs as) from the owner key.
//
// The connection should be a read-only ClickHouse user; the guard and the
// readonly=2 setting are belt and braces, not a substitute for grants.
package chquery

import (
	"context"
	"fmt"
	"reflect"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/reliant-labs/forge/pkg/alerting"
)

// Conn is the part of clickhouse.Conn the querier uses.
type Conn interface {
	Query(ctx context.Context, query string, args ...any) (driver.Rows, error)
}

// Querier implements alerting.Querier.
type Querier struct{ conn Conn }

var _ alerting.Querier = (*Querier)(nil)

// New wraps conn.
func New(conn Conn) *Querier { return &Querier{conn: conn} }

// Query runs the statement with opts applied as ClickHouse settings and returns
// every row. The owner key is unused.
func (q *Querier) Query(ctx context.Context, _ string, query string, opts alerting.QueryOptions) ([]alerting.Row, error) {
	return Run(ctx, q.conn, query, opts)
}

// Run is the shared row-reading half: a multi-owner Querier calls it with the
// connection it chose.
func Run(ctx context.Context, conn Conn, query string, opts alerting.QueryOptions) ([]alerting.Row, error) {
	if err := alerting.ValidateExpr(query); err != nil {
		return nil, err
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings(opts.Settings())))
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return Scan(rows)
}

// Scan reads every row into alerting.Rows. A Querier that must choose its own
// settings (see alerting.QueryOptions.Limits) reuses this and sends its own.
func Scan(rows driver.Rows) ([]alerting.Row, error) {
	names, types := rows.Columns(), rows.ColumnTypes()
	var out []alerting.Row
	for rows.Next() {
		ptrs := make([]any, len(names))
		for i := range ptrs {
			ptrs[i] = reflect.New(types[i].ScanType()).Interface()
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("chquery: scanning: %w", err)
		}
		row := make(alerting.Row, len(names))
		for i, n := range names {
			row[n] = reflect.ValueOf(ptrs[i]).Elem().Interface()
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
