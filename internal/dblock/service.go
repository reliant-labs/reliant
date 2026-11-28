// Copyright (c) 2025 Reliant Labs
//
// Package dblock takes the session-level Postgres advisory locks that elect one
// replica to run a periodic sweep. It owns the registry of their keys, so two
// sweeps cannot pick the same number, and the one place the lock SQL lives.
//
// The transaction-scoped advisory locks that serialize writers inside a
// transaction are not here: each is taken by a store function next to the rows
// it protects (store.LockProjectCatalog, store.LockMachineCheckouts, ...), and
// must run inside the caller's transaction.
//
// forge:provided because Deps.Sessions is the repo's session pool, which no
// graph node produces. forge's orm.Context resolution does not apply: the
// pool is a *sql.DB of dedicated sessions, not the ORM, and *db.Repo cannot
// be the node that satisfies Sessions because it builds its own locker from
// that pool (a cycle). The providers that lock (providers_api.go) and db.Repo
// build it with dblock.New(Deps).
//
//forge:provided
package dblock

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/reliant-labs/reliant/pkg/config"
)

// Key names one leader-elected job. The values are the keys production already
// holds, so a rolling deploy where old and new replicas overlap still elects
// exactly one leader across both.
type Key int64

const (
	// WorktreeSweep elects the replica that sweeps worktrees for reclaim.
	WorktreeSweep Key = 0x7265_6c69_616e_7401 // "reliant\x01"
	// AlertReceivers elects the replica that reconciles the control plane's
	// alert receivers: two sweeps provisioning one activation could each
	// rotate its token and leave the control plane holding the other's.
	AlertReceivers Key = 0x7265_6c69_616e_7402 // "reliant\x02"
	// ChatUpdateHeads elects the replica that backfills chats' materialized
	// snapshot heads.
	ChatUpdateHeads Key = 0x7265_6c69_616e_7403 // "reliant\x03"
	// GtmAssetSweep elects the replica that deletes GTM assets left PENDING
	// (internal/gtm/assetstore's Sweeper).
	GtmAssetSweep Key = 0x7265_6c69_616e_7404 // "reliant\x04"
)

// Deps is what the lock service is built from.
type Deps struct {
	// forge:optional-dep
	Logger *slog.Logger
	// forge:optional-dep
	Config *config.Config

	// Sessions is the pool the lock's connection comes from. Required.
	Sessions Sessions
}

// locker takes advisory locks on connections from its session pool.
//
//forge:contract
type locker struct {
	deps Deps
}

// New builds the lock service from deps.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Sessions == nil {
		return nil, errors.New("dblock: Deps.Sessions is required")
	}
	return &locker{deps: deps}, nil
}

// TryLock takes the session-level lock on key without waiting, on a dedicated
// connection, so exactly one replica at a time holds it. ok is false, with a
// no-op release, when another session holds it. The lock dies with the
// connection, so a crashed holder never strands it; release unlocks it and
// returns the connection, and must be called once the work is done.
func (l *locker) TryLock(ctx context.Context, key Key) (release func(), ok bool, err error) {
	conn, err := l.deps.Sessions.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	return tryOn(ctx, conn, int64(key))
}

// TryLockNamed is TryLock for a job with one lock per instance, named by
// text rather than drawn from the Key registry: the merge queue's lead of one
// repository ("mergequeue:<repo id>"). The key is Postgres's
// hashtextextended(name, 0), so every replica derives the same one. Name such
// locks with a prefix of their own, so two jobs' names cannot meet.
func (l *locker) TryLockNamed(ctx context.Context, name string) (release func(), ok bool, err error) {
	conn, err := l.deps.Sessions.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var key int64
	if err := conn.QueryRowContext(ctx, `SELECT hashtextextended($1, 0)`, name).Scan(&key); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	return tryOn(ctx, conn, key)
}

// tryOn takes the session-level lock on key on conn, which it owns: closed
// unless the lock is held, and then by the release.
func tryOn(ctx context.Context, conn *sql.Conn, key int64) (release func(), ok bool, err error) {
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !got {
		_ = conn.Close()
		return func() {}, false, nil
	}
	return func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, key)
		_ = conn.Close()
	}, true, nil
}
