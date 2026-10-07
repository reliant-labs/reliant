// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// The daemons table is a read replica of the control plane's daemon set for
// everything except liveness (daemon_attachment, which this service owns).
// The control plane is the authority on which machines EXIST, what they are
// called, and what lifecycle phase they are in; this file holds the three
// writes that keep the replica honest about that:
//
//   - UpsertDaemonIdentity: a machine exists (fast path, from an
//     identity-bearing lifecycle event at create time).
//   - RemoveDaemon: a machine no longer exists (fast path, from a removed
//     event at delete time).
//   - ApplyDaemonRegistrySnapshot: the reconcile. One owner's full set, which
//     repairs whatever the two fast paths lost.
//
// Before these existed the replica could only ever grow, and only when a
// machine's pod reached the gateway: a deleted machine stayed listed forever,
// and a new one was invisible for the minutes it took to boot
// (MACHINE_LIST_BUGS_2026-10-07).

// DaemonIdentity is what the control plane knows about a daemon's existence.
type DaemonIdentity struct {
	DaemonID   string
	UserID     string
	Name       string
	DaemonType string
	// CreatedAt is the control plane's creation time. Zero means "now".
	CreatedAt time.Time
}

// RegistrySnapshotApply is one owner's authoritative daemon set, as applied by
// ApplyDaemonRegistrySnapshot.
type RegistrySnapshotApply struct {
	UserID  string
	Daemons []RegistrySnapshotDaemon
	// KeepCreatedAfter protects rows this registry created recently from
	// removal even when the snapshot does not list them. The control plane
	// learns of a self-hosted daemon only after the gateway has registered
	// it here, so a snapshot read in that gap would otherwise delete a daemon
	// that is seconds old. Callers pass the snapshot's read time minus a
	// grace period.
	KeepCreatedAfter time.Time
	// KeepAttachedSince protects rows with a live attachment lease: a daemon
	// attached right now is evidently real, whatever the snapshot says, and
	// deleting its row would hide a connected machine. Callers pass now minus
	// the attachment freshness window.
	KeepAttachedSince time.Time
}

// RegistrySnapshotDaemon is one daemon in a RegistrySnapshotApply. Lifecycle
// is nil for daemons with none to mirror (every self-hosted daemon).
type RegistrySnapshotDaemon struct {
	Identity  DaemonIdentity
	Lifecycle *DaemonLifecycleUpdate
}

// RegistrySnapshotResult reports what a snapshot changed, so the caller can
// tell the owner's clients to refetch only when something did.
type RegistrySnapshotResult struct {
	Upserted []string
	Removed  []string
}

// Changed reports whether the snapshot changed any row.
func (r RegistrySnapshotResult) Changed() bool {
	return len(r.Upserted) > 0 || len(r.Removed) > 0
}

// execer is the write surface shared by the pool and a transaction, so the
// single-row writes below run unchanged inside the snapshot's transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// UpsertDaemonIdentity creates the registry row for a daemon the control plane
// reports, or brings the name on an existing row up to date. Returns whether a
// row was written.
//
// Deliberately narrow on conflict:
//
//   - user_id is NEVER rewritten. A row moving between owners on the strength
//     of an event is not something a mirror should do in place; if the two
//     sides ever disagree, the snapshot removes the row from the wrong owner
//     and creates it for the right one, each step scoped to one owner.
//   - Hostname, platform and the rest are the daemon's own registration and
//     are left to UpsertDaemon.
//   - daemon_type is filled only when the row has none.
//
// The conflict update is conditional, so an unchanged daemon costs a no-op
// rather than a write — the snapshot re-asserts every daemon on every sweep,
// and rewriting updated_at each time would reorder the list for nothing.
func (r *Repo) UpsertDaemonIdentity(ctx context.Context, id DaemonIdentity) (bool, error) {
	return r.upsertDaemonIdentity(ctx, r.DB, id)
}

func (r *Repo) upsertDaemonIdentity(ctx context.Context, ex execer, id DaemonIdentity) (bool, error) {
	if id.DaemonID == "" {
		return false, fmt.Errorf("daemon ID cannot be empty")
	}
	if id.UserID == "" {
		return false, fmt.Errorf("daemon user ID cannot be empty")
	}
	now := time.Now().UTC()
	createdAt := id.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	var daemonType any
	if id.DaemonType != "" {
		daemonType = id.DaemonType
	}

	query := r.bindQuery(`
		INSERT INTO daemons (id, user_id, name, daemon_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			name = COALESCE(NULLIF(excluded.name, ''), daemons.name),
			daemon_type = COALESCE(daemons.daemon_type, excluded.daemon_type),
			updated_at = excluded.updated_at
		WHERE (excluded.name <> '' AND daemons.name IS DISTINCT FROM excluded.name)
		   OR (daemons.daemon_type IS NULL AND excluded.daemon_type IS NOT NULL)
	`)
	res, err := ex.ExecContext(ctx, query, id.DaemonID, id.UserID, id.Name, daemonType, createdAt, now)
	if err != nil {
		return false, fmt.Errorf("failed to upsert daemon identity %s: %w", id.DaemonID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed reading identity upsert result for %s: %w", id.DaemonID, err)
	}
	return n > 0, nil
}

// RemoveDaemon deletes a daemon's registry row and its attachment lease, and
// returns the owner the row belonged to so the caller can tell that owner's
// clients. removed is false when there was no row — removal is idempotent,
// and a removed event for a daemon this registry never heard of is normal.
//
// The attachment goes too: a lease for a daemon that does not exist is
// meaningless, and a stale one would keep the reap loop as its only cleanup.
// Connector grants go with the row through their ON DELETE CASCADE, which is
// right — a grant to a machine that no longer exists grants nothing.
func (r *Repo) RemoveDaemon(ctx context.Context, daemonID string) (string, bool, error) {
	if daemonID == "" {
		return "", false, fmt.Errorf("daemon ID cannot be empty")
	}
	tx, err := r.DB.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin daemon removal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	userID, removed, err := r.removeDaemon(ctx, tx, daemonID)
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit daemon removal %s: %w", daemonID, err)
	}
	return userID, removed, nil
}

func (r *Repo) removeDaemon(ctx context.Context, tx *sql.Tx, daemonID string) (string, bool, error) {
	if _, err := tx.ExecContext(ctx, r.bindQuery(`DELETE FROM daemon_attachment WHERE daemon_id = ?`), daemonID); err != nil {
		return "", false, fmt.Errorf("deleting attachment for removed daemon %s: %w", daemonID, err)
	}
	var userID string
	err := tx.QueryRowContext(ctx, r.bindQuery(`DELETE FROM daemons WHERE id = ? RETURNING user_id`), daemonID).Scan(&userID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("deleting daemon %s: %w", daemonID, err)
	}
	return userID, true, nil
}

// ApplyDaemonRegistrySnapshot reconciles one owner's registry rows against the
// control plane's authoritative set, in one transaction:
//
//  1. every daemon in the set gets its identity upserted (created if missing),
//     and its lifecycle applied through the same newest-wins guard as a
//     lifecycle event;
//  2. every row of this owner NOT in the set is removed — unless it was
//     created after KeepCreatedAfter or holds an attachment lease fresher than
//     KeepAttachedSince. Those two guards are what make "absent from the
//     control plane's set" safe to act on: the control plane hears about a
//     self-hosted daemon only after it has registered here, and a connected
//     daemon is real whatever a snapshot read a moment ago says.
//
// Scoped to one owner throughout. Rows of other owners are never read or
// written, so a snapshot can only ever affect the user it names.
func (r *Repo) ApplyDaemonRegistrySnapshot(ctx context.Context, snap RegistrySnapshotApply) (RegistrySnapshotResult, error) {
	var result RegistrySnapshotResult
	if snap.UserID == "" {
		return result, fmt.Errorf("snapshot user ID cannot be empty")
	}

	tx, err := r.DB.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin registry snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	keep := make([]string, 0, len(snap.Daemons))
	for _, d := range snap.Daemons {
		ident := d.Identity
		ident.UserID = snap.UserID
		keep = append(keep, ident.DaemonID)

		wrote, err := r.upsertDaemonIdentity(ctx, tx, ident)
		if err != nil {
			return RegistrySnapshotResult{}, err
		}
		if d.Lifecycle != nil {
			lc := *d.Lifecycle
			lc.DaemonID = ident.DaemonID
			applied, err := r.applyDaemonLifecycle(ctx, tx, lc)
			if err != nil {
				return RegistrySnapshotResult{}, err
			}
			wrote = wrote || applied
		}
		if wrote {
			result.Upserted = append(result.Upserted, ident.DaemonID)
		}
	}

	rows, err := tx.QueryContext(ctx, r.bindQuery(`
		SELECT d.id FROM daemons d
		WHERE d.user_id = ?
		  AND NOT (d.id = ANY(COALESCE(?::text[], ARRAY[]::text[])))
		  AND d.created_at < ?
		  AND NOT EXISTS (
			SELECT 1 FROM daemon_attachment a
			WHERE a.daemon_id = d.id AND a.last_stream_activity > ?
		  )
		FOR UPDATE OF d
	`), snap.UserID, keep, snap.KeepCreatedAfter, snap.KeepAttachedSince)
	if err != nil {
		return RegistrySnapshotResult{}, fmt.Errorf("listing registry rows absent from snapshot: %w", err)
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return RegistrySnapshotResult{}, fmt.Errorf("scanning absent registry row: %w", err)
		}
		stale = append(stale, id)
	}
	if err := rows.Close(); err != nil {
		return RegistrySnapshotResult{}, fmt.Errorf("closing absent registry rows: %w", err)
	}
	for _, id := range stale {
		if _, removed, err := r.removeDaemon(ctx, tx, id); err != nil {
			return RegistrySnapshotResult{}, err
		} else if removed {
			result.Removed = append(result.Removed, id)
		}
	}

	if err := tx.Commit(); err != nil {
		return RegistrySnapshotResult{}, fmt.Errorf("commit registry snapshot for %s: %w", snap.UserID, err)
	}
	return result, nil
}
