// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"fmt"
	"sync"

	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

// runControlLockTimeout bounds how long a caller waits for another request's
// run-control critical section. It is a ceiling for a wedged holder, not a
// budget a healthy one approaches: a pause or a send holds the lock for one
// Temporal round trip plus a few row writes.
const runControlLockTimeout = "15s"

// LockChatRunControl takes the chat's run-control lock and returns the func
// that releases it. Release is idempotent.
//
// Pause, resume and SendMessage's resume-or-wake routing each pair a Temporal
// signal with a workflow-status write, and Postgres and Temporal cannot commit
// together. Unserialized, a send can read the status in the gap between a
// pause's signal and its write, see "running", and route the message as a
// wake — which never releases the pause gate. That stranded chat 264b5697 for
// 4.5 minutes. See runs.Service.LockRunControl.
//
// The lock is a Postgres transaction-scoped advisory lock, so it holds across
// every API server and worker replica sharing this database. The transaction
// exists only to own the lock: it is never placed in the returned context's
// path, so the caller's own reads and writes run (and commit) independently of
// it. READ COMMITTED keeps it out of the SERIALIZABLE predicate-lock graph.
//
// The lock is released by release, or when ctx ends — database/sql rolls the
// transaction back the moment its context is done, and a request that has
// gone away no longer needs exclusion. While held, and while waiting, it
// occupies one pooled connection.
func (r *Repo) LockChatRunControl(ctx context.Context, chatID string) (func(), error) {
	if r.DB == nil {
		return nil, errors.New("database connection not initialized")
	}
	// Taking it inside a caller's transaction would release the lock at THAT
	// transaction's commit, which is not the critical section the caller means.
	if _, ok := ctx.Value(txKey).(pgdb.DBTX); ok {
		return nil, errors.New("run-control lock must not be taken inside a transaction")
	}

	tx, err := r.DB.BeginTxWithOptions(ctx, TxOptions{Isolation: IsolationReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin run-control lock transaction: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '`+runControlLockTimeout+`'`); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("set run-control lock timeout: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "run-control:"+chatID); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("acquire run-control lock: %w", err)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			// Nothing was written; rolling back is how the lock is let go.
			// ErrTxDone just means ctx ending already released it.
			_ = tx.Rollback()
		})
	}, nil
}
