// forge:deps — consumer-declared interfaces for this package's Deps.
// Copyright (c) 2025 Reliant Labs

package dblock

import (
	"context"
	"database/sql"
)

// Sessions hands out a Postgres connection that stays the same backend for as
// long as the caller holds it. *sql.DB satisfies it.
//
// A session-level advisory lock lives on one backend, so the connection must
// not be shared with a transaction-pooling PgBouncer: the lock would be taken
// on one backend and released (or re-entered by another replica) on another.
// The caller therefore passes the pool built from DatabaseConfig.DirectURL when
// the main pool is transaction-pooled, and the main pool otherwise
// (db.Repo.SessionPool).
type Sessions interface {
	Conn(ctx context.Context) (*sql.Conn, error)
}
