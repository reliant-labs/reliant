// Package shapes holds every pre-v0.1.47 pkg/orm transaction call shape, so
// `forge fix tx-isolation` can be run over it and the result proven to begin
// each transaction exactly as before.
package shapes

import (
	"context"
	"database/sql"

	"github.com/reliant-labs/forge/pkg/orm"
)

// Probe runs fn inside one transaction and reports what it saw.
type Probe func(ctx context.Context, q orm.Context) error

// Repo has its own RunTx, with its own semantics: the codemod must leave
// calls to it alone and rewrite only the orm call inside it.
type Repo struct{ c *orm.Client }

func (r *Repo) RunTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.c.RunTx(ctx, fn)
}

// unsetOptions left everything to the old defaults: SERIALIZABLE, 7 attempts.
var unsetOptions = orm.TxOptions{Isolation: orm.IsolationDefault}

// Shapes are the call shapes, by name.
func Shapes(c *orm.Client) map[string]func(ctx context.Context, p Probe) error {
	var db orm.Context = c
	repo := &Repo{c: c}
	inCtx := func(ctx context.Context, p Probe) func(context.Context) error {
		return func(ctx context.Context) error { return p(ctx, c) }
	}
	return map[string]func(ctx context.Context, p Probe) error{
		"RunTx": func(ctx context.Context, p Probe) error {
			return c.RunTx(ctx, inCtx(ctx, p))
		},
		"Context.RunTx": func(ctx context.Context, p Probe) error {
			return db.RunTx(ctx, inCtx(ctx, p))
		},
		"Repo.RunTx": func(ctx context.Context, p Probe) error {
			return repo.RunTx(ctx, inCtx(ctx, p))
		},
		"RunTxWithOptions{}": func(ctx context.Context, p Probe) error {
			return c.RunTxWithOptions(ctx, orm.TxOptions{}, inCtx(ctx, p))
		},
		"RunTxWithOptions{ReadCommitted}": func(ctx context.Context, p Probe) error {
			return c.RunTxWithOptions(ctx, orm.TxOptions{Isolation: orm.IsolationReadCommitted}, inCtx(ctx, p))
		},
		"RunTxWithOptions{ReadCommitted,1}": func(ctx context.Context, p Probe) error {
			return c.RunTxWithOptions(ctx, orm.TxOptions{Isolation: orm.IsolationReadCommitted, MaxAttempts: 1}, inCtx(ctx, p))
		},
		"RunTxWithOptions{RepeatableRead,3}": func(ctx context.Context, p Probe) error {
			return c.RunTxWithOptions(ctx, orm.TxOptions{Isolation: orm.IsolationRepeatableRead, MaxAttempts: 3}, inCtx(ctx, p))
		},
		"RunTxWithOptions{ReadOnly}": func(ctx context.Context, p Probe) error {
			return c.RunTxWithOptions(ctx, orm.TxOptions{ReadOnly: true}, inCtx(ctx, p))
		},
		"RunTxWithOptions(var)": func(ctx context.Context, p Probe) error {
			return db.RunTxWithOptions(ctx, unsetOptions, inCtx(ctx, p))
		},
		"RunTxReadOnly": func(ctx context.Context, p Probe) error {
			return c.RunTxReadOnly(ctx, inCtx(ctx, p))
		},
		"RunTransaction": func(ctx context.Context, p Probe) error {
			return c.RunTransaction(ctx, func(tx orm.Context) error { return p(ctx, tx) })
		},
		"RunTransactionWithOptions(nil)": func(ctx context.Context, p Probe) error {
			return c.RunTransactionWithOptions(ctx, nil, func(tx orm.Context) error { return p(ctx, tx) })
		},
		"RunTransactionWithOptions{Serializable}": func(ctx context.Context, p Probe) error {
			return c.RunTransactionWithOptions(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx orm.Context) error { return p(ctx, tx) })
		},
		"BeginTx(nil)": func(ctx context.Context, p Probe) error {
			tx, err := c.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			return p(ctx, tx)
		},
		"BeginTx{RepeatableRead,ReadOnly}": func(ctx context.Context, p Probe) error {
			tx, err := c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			// The *Tx BeginTx returned joins, as it always did.
			return tx.RunTx(ctx, func(ctx context.Context) error { return p(ctx, tx) })
		},
		"BeginDetached{}": func(ctx context.Context, p Probe) error {
			tx, err := c.BeginDetached(ctx, orm.TxOptions{})
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			return p(ctx, tx)
		},
		"BeginDetached{ReadCommitted}": func(ctx context.Context, p Probe) error {
			tx, err := db.BeginDetached(ctx, orm.TxOptions{Isolation: orm.IsolationReadCommitted})
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			return p(ctx, tx)
		},
	}
}

// PlainSQL is database/sql's own BeginTx, which the codemod must not touch.
func PlainSQL(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	return tx.Rollback()
}
