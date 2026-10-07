// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const providerKeyLockTimeout = "15s"

// LockProviderKey serializes credential replacement for one user's provider
// across every replica sharing this database, and returns the idempotent func
// that releases it.
//
// It is a transaction-scoped Postgres advisory lock held by a transaction that
// writes nothing: the caller's own reads and writes run on other connections.
// The lock is also released when ctx ends.
func (r *Repo) LockProviderKey(ctx context.Context, userID, provider string) (func(), error) {
	if r.DB == nil {
		return nil, errors.New("database connection not initialized")
	}
	tx, err := r.DB.BeginTxWithOptions(ctx, TxOptions{Isolation: IsolationReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin provider-key lock transaction: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '`+providerKeyLockTimeout+`'`); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("set provider-key lock timeout: %w", err)
	}
	name := fmt.Sprintf("provider-key:%d:%s:%s", len(userID), userID, provider)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, name); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("acquire provider-key lock: %w", err)
	}
	var once sync.Once
	return func() { once.Do(func() { _ = tx.Rollback() }) }, nil
}
