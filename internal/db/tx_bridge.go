// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"

	"github.com/reliant-labs/forge/pkg/orm"
)

// One transaction, either entry point.
//
// The Repository's queries are internal/store's (the forge ORM), on ONE pool,
// and share ONE notion of "the transaction ctx carries": forge's. Repo.RunTx
// opens its transaction through the ORM client (beginSharedTx) and runs f
// under the ctx tx.Join hands it, so every generated delegate and
// _repo_ext.go query called with that ctx joins it, and a Repo.RunTx inside
// orm.Client.RunTx (or inside another Repo.RunTx) joins rather than begins.
//
// Repo.RunTx keeps its own attempt loop (isRetryableError: 40001 and 40P01
// as pkg/orm retries, plus 23505, 25P02 and 40002; bounded by the policy's
// attempt budget) and opens each attempt with the ORM's non-retrying
// BeginDetached; orm.Client.RunTx retries 40001/40P01 only. Whichever opens
// the transaction owns its retry policy, and a nested RunTx of either kind
// joins it.

// WrappedDBTX holds the Repository's pool and the forge ORM client over it:
// the transaction Repo.RunTx opens and joins, DB(ctx) for a raw statement in
// whatever transaction ctx carries (tests), and the detached
// BeginTxWithOptions.
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
// SERIALIZABLE. opts is one attempt: BeginDetached never retries and never
// joins; runTxWithRetries does the former, RunTx the latter.
func (w *WrappedDBTX) beginSharedTx(ctx context.Context, opts orm.TxOptions) (*orm.Tx, error) {
	return w.orm.BeginDetached(ctx, opts)
}
