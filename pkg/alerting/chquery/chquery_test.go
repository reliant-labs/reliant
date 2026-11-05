// Copyright (c) 2025 Reliant Labs

package chquery

import (
	"context"
	"errors"
	"testing"

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
