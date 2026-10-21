// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
)

// One transaction, either layer.
//
// The Repository (hand SQL) and internal/store (the forge ORM) run
// on ONE pool and share ONE notion of "the transaction ctx carries": forge's.
// Repo.RunTx opens its transaction through the ORM client (beginSharedTx)
// and runs f under the ctx tx.RunTx hands it, so every generated delegate and
// _repo_ext.go query called with that ctx joins it. In the other direction,
// WrappedDBTX resolves each statement against the transaction forge carries
// in ctx, so a Repository call made inside orm.Client.RunTx — or inside any
// Repo.RunTx, which then joins rather than begins — runs in that
// transaction. Without this a call through the other layer ran on the pool:
// outside the transaction, and blocked forever on a row it had locked.
//
// Repo.RunTx keeps its own attempt loop (isRetryableError, maxRetries) and
// opens each attempt with the ORM's non-retrying BeginTx; orm.Client.RunTx
// retries 40001/40P01 only. Whichever layer opens the transaction owns its
// retry policy, and a nested RunTx of either kind joins it.

// WrappedDBTX is the executor every hand-written Repository statement runs on: the transaction ctx carries, or the pool, with
// statement-level retries outside a transaction.
type WrappedDBTX struct {
	db *sql.DB
	// orm is the forge ORM client over db. Its transactions are the
	// Repository's (beginSharedTx), and the transaction it carries in ctx is
	// the one DB(ctx) resolves to.
	orm *orm.Client
}

// sqlExecutor is what a Repository statement runs on: *sql.DB, or the
// transaction ctx carries.
type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// DB returns the executor for a statement run with ctx: the transaction ctx
// carries on this pool, or the pool.
func (w *WrappedDBTX) DB(ctx context.Context) sqlExecutor {
	if w.inTx(ctx) {
		return ambientConn{w.orm}
	}
	return w.db
}

// inTx reports whether ctx carries an active transaction, opened by either
// layer, that this Repository's statements join.
//
// orm.InTx answers for the innermost transaction on any database. Every
// forge transaction in this process is on this pool, so that is this pool's;
// once forge's orm.Client.InTx (per pool) is pinned, use it here.
func (w *WrappedDBTX) inTx(ctx context.Context) bool {
	return orm.InTx(ctx)
}

// beginSharedTx begins one Repo.RunTx attempt: a forge transaction on this
// pool, at opts' isolation, READ ONLY DEFERRABLE when opts.ReadOnly under
// SERIALIZABLE (see TxOptions.ReadOnly). The ORM's BeginTx never retries
// and never joins; runTxWithRetries does the former, RunTxWithOptions the
// latter.
func (w *WrappedDBTX) beginSharedTx(ctx context.Context, opts TxOptions) (*orm.Tx, error) {
	tx, err := w.orm.BeginTx(ctx, &sql.TxOptions{
		Isolation: opts.Isolation.sqlLevel(),
		ReadOnly:  opts.ReadOnly,
	})
	if err != nil {
		return nil, err
	}
	if opts.ReadOnly && opts.Isolation.sqlLevel() == sql.LevelSerializable {
		// database/sql has no DEFERRABLE flag; see BeginTxWithOptions.
		if _, err := tx.Exec(ctx, "SET TRANSACTION READ ONLY DEFERRABLE"); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("set read-only deferrable: %w", err)
		}
	}
	return tx, nil
}

// ambientConn runs statements through the ORM client's raw passthroughs
// (Exec/Query/QueryRow: native $N placeholders, bun's formatter bypassed),
// which run on the transaction ctx carries for the pool.
//
// It stands in for forge's orm.Client.SQLConn(ctx), which returns that
// transaction's *sql.Tx itself and is not in the pinned forge yet; when it
// is, DB returns w.orm.SQLConn(ctx) and this goes.
type ambientConn struct{ c *orm.Client }

func (a ambientConn) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return a.c.Exec(ctx, query, args...)
}

func (a ambientConn) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return a.c.Query(ctx, query, args...)
}

func (a ambientConn) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return a.c.QueryRow(ctx, query, args...)
}
