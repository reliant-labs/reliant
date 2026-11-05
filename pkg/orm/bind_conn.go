package orm

import (
	"context"
	"database/sql"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/schema"
)

// ─── running bun's statements with their values bound ──────────────────────
//
// bun renders a statement, runs its query hooks, then asks for the
// connection and runs the text there with no arguments. Two seams carry a
// statement to binding (bind.go):
//
//   - bindHook, a bun query hook, puts the query that rendered the text into
//     the ctx bun runs it with. That is event.IQuery — for Count and Exists
//     bun's own wrapper query, so it is always the query whose rendering IS
//     the text.
//   - bindConn is the connection bun is handed: by ambientResolver for every
//     query built off a Client's bun handle, and by Tx.Bun()'s builders for
//     queries pinned to an explicit transaction. It re-renders that query
//     with the binding dialect and runs the bound statement.
//
// A statement that arrives with arguments already, with no query in ctx, or
// that does not bind (bind.go) runs as given.

// renderKey is the ctx key of the query bun is running.
type renderKey struct{}

// inlineKey marks a ctx whose statements are not bound.
type inlineKey struct{}

// InlineValues returns ctx with parameter binding off: every statement a
// Client's bun handle runs with it is sent exactly as bun formats it, values
// in the text. It is the escape hatch for a statement binding mishandles;
// to keep a single value in the text, use Literal.
func InlineValues(ctx context.Context) context.Context {
	return context.WithValue(ctx, inlineKey{}, true)
}

// bindHook records the query bun is about to run. See the file comment.
type bindHook struct{}

func (bindHook) BeforeQuery(ctx context.Context, e *bun.QueryEvent) context.Context {
	var q bun.Query
	if ctx.Value(inlineKey{}) == nil {
		q = e.IQuery
	}
	// A statement with no query of its own (bun's raw ExecContext) must not
	// inherit the one an enclosing statement left in ctx.
	if q == nil && ctx.Value(renderKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, renderKey{}, q)
}

func (bindHook) AfterQuery(context.Context, *bun.QueryEvent) {}

// bindConn is the bun.IConn statements run on: conn, with the values of the
// statement bun rendered bound.
type bindConn struct {
	conn    bun.IConn // the *sql.DB pool or the *sql.Tx
	dialect *forgeDialect
}

// bound is the statement to send for query, which bun rendered and is running
// without arguments.
func (c *bindConn) bound(ctx context.Context, query string, args []any) (string, []any) {
	if len(args) > 0 {
		return query, args
	}
	q, ok := ctx.Value(renderKey{}).(schema.QueryAppender)
	if !ok {
		return query, nil
	}
	st := getBindState(c.dialect)
	defer st.release()
	text, err := q.AppendQuery(schema.NewQueryGen(&st.dialect), st.text[:0])
	st.text = text
	if err != nil {
		return query, nil
	}
	stmt, bargs, ok := st.bind(text, query)
	if !ok {
		return query, nil
	}
	return stmt, bargs
}

func (c *bindConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	query, args = c.bound(ctx, query, args)
	return c.conn.QueryContext(ctx, query, args...)
}

func (c *bindConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	query, args = c.bound(ctx, query, args)
	return c.conn.ExecContext(ctx, query, args...)
}

func (c *bindConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	query, args = c.bound(ctx, query, args)
	return c.conn.QueryRowContext(ctx, query, args...)
}

var _ bun.IConn = (*bindConn)(nil)

// txBun is the bun handle of an explicit transaction (Tx.Bun()): the bun.Tx,
// with every query it builds pinned to the transaction through a bindConn.
type txBun struct {
	bun.Tx
	conn *bindConn
}

func (t txBun) NewSelect() *bun.SelectQuery { return t.Tx.NewSelect().Conn(t.conn) }
func (t txBun) NewInsert() *bun.InsertQuery { return t.Tx.NewInsert().Conn(t.conn) }
func (t txBun) NewUpdate() *bun.UpdateQuery { return t.Tx.NewUpdate().Conn(t.conn) }
func (t txBun) NewDelete() *bun.DeleteQuery { return t.Tx.NewDelete().Conn(t.conn) }
func (t txBun) NewMerge() *bun.MergeQuery   { return t.Tx.NewMerge().Conn(t.conn) }
func (t txBun) NewValues(model any) *bun.ValuesQuery {
	return t.Tx.NewValues(model).Conn(t.conn)
}

func (t txBun) NewRaw(query string, args ...any) *bun.RawQuery {
	return t.Tx.NewRaw(query, args...).Conn(t.conn)
}

var _ bun.IDB = txBun{}
