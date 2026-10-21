// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"

	"github.com/reliant-labs/forge/pkg/orm"
)

// One transaction, either layer.
//
// The Repository (hand SQL) and internal/store (the forge ORM) run
// on ONE pool and share ONE notion of "the transaction ctx carries": forge's.
// Repo.RunTx opens its transaction through the ORM client (beginSharedTx)
// and runs f under the ctx tx.Join hands it, so every generated delegate and
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

// WrappedDBTX is the executor every hand-written Repository statement runs
// on: the transaction ctx carries, or the pool, with statement-level retries
// outside a transaction.
type WrappedDBTX struct {
	db *sql.DB
	// orm is the forge ORM client over db. Its transactions are the
	// Repository's (beginSharedTx), and the transaction it carries in ctx is
	// the one DB(ctx) resolves to.
	orm *orm.Client
}

// DB returns the executor for a statement run with ctx: the *sql.Tx of the
// transaction ctx carries on this pool, or the pool (orm.Client.SQLConn).
func (w *WrappedDBTX) DB(ctx context.Context) orm.SQLConn {
	return w.orm.SQLConn(ctx)
}

// inTx reports whether ctx carries an active transaction on this pool,
// opened by either layer, that this Repository's statements join.
func (w *WrappedDBTX) inTx(ctx context.Context) bool {
	return w.orm.InTx(ctx)
}

// beginSharedTx begins one Repo.RunTx attempt: a forge transaction on this
// pool, at opts' isolation, READ ONLY DEFERRABLE when opts.ReadOnly under
// SERIALIZABLE (see TxOptions.ReadOnly). BeginDetached never retries and
// never joins; runTxWithRetries does the former, RunTxWithOptions the
// latter.
func (w *WrappedDBTX) beginSharedTx(ctx context.Context, opts TxOptions) (*orm.Tx, error) {
	return w.orm.BeginDetached(ctx, opts.ormPolicy())
}
