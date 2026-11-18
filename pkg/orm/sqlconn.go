package orm

import (
	"context"
	"database/sql"
)

// database/sql interop: plain SQL on the same pool joins RunTx.
//
// A project rarely starts on the ORM. It arrives with hand-written
// database/sql code, or sqlc, already on the pool the Client is built from,
// and ports one query at a time — so for a while one transaction has
// statements from both layers in it. That only works if the two agree on
// what "the transaction ctx carries" is. Without these, a sqlc call made
// inside RunTx runs on the pool: outside the transaction, so it is not
// rolled back with it, and blocked forever (two connections, one goroutine —
// a deadlock postgres cannot see) if it touches a row the transaction has
// locked.
//
// The other direction needs no hook: a layer that opens its own transaction
// through BeginTx and hands its work the ctx from tx.RunTx is carrying a
// forge transaction, so generated delegates, stores and pkg/crud join it,
// and SQLConn / DBTX resolve to it as well.

// SQLConn is the executor surface *sql.DB and *sql.Tx share. It is the shape
// sqlc's generated DBTX interface names, so either value — and DBTX's —
// satisfies a sqlc Queries directly.
type SQLConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SQLConn returns the database/sql executor ctx resolves to on c's pool: the
// *sql.Tx of the active transaction ctx carries for this database (from
// RunTx, or a BeginTx'd *Tx's RunTx), else the *sql.DB itself. Statements run
// on it exactly as generated delegates would run with the same ctx. A
// statement prepared on it inside a transaction is bound to that
// transaction, as *sql.Tx.PrepareContext is.
//
// Resolve it per use, with the ctx the statement runs with; a value kept
// past the transaction's end is a finished *sql.Tx. Use DBTX for a value to
// hold.
func (c *Client) SQLConn(ctx context.Context) SQLConn {
	if st := ambientTx(ctx, c.bun.DB); st != nil {
		return st.tx.Tx
	}
	return c.bun.DB
}

// InTx reports whether ctx carries an active transaction on c's pool — the
// one c's queries, SQLConn and DBTX would join. Unlike the package-level
// InTx, which answers for the innermost transaction on any database, this
// is the check to make before joining instead of beginning: a transaction
// on another pool is no reason not to open one on this one.
func (c *Client) InTx(ctx context.Context) bool {
	return ambientTx(ctx, c.bun.DB) != nil
}

// DBTX returns a SQLConn that resolves the transaction on every call: each
// method runs on SQLConn(ctx) for the ctx it is given. Construct sqlc's
// Queries over it once (pgdb.New(client.DBTX())) and every query joins the
// RunTx transaction its ctx carries, like the ORM's own.
func (c *Client) DBTX() SQLConn { return ambientSQL{c} }

// ambientSQL is DBTX's value.
type ambientSQL struct{ c *Client }

func (a ambientSQL) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return a.c.SQLConn(ctx).ExecContext(ctx, query, args...)
}

func (a ambientSQL) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return a.c.SQLConn(ctx).PrepareContext(ctx, query)
}

func (a ambientSQL) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return a.c.SQLConn(ctx).QueryContext(ctx, query, args...)
}

func (a ambientSQL) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return a.c.SQLConn(ctx).QueryRowContext(ctx, query, args...)
}

var (
	_ SQLConn = (*sql.DB)(nil)
	_ SQLConn = (*sql.Tx)(nil)
	_ SQLConn = ambientSQL{}
)
