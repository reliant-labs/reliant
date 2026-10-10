// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/json"
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
// right — a grant to a machine that no longer exists grants nothing. Every
// other binding to the machine is released in the same transaction
// (releaseDaemonBindings).
func (r *Repo) RemoveDaemon(ctx context.Context, daemonID string) (string, bool, error) {
	if daemonID == "" {
		return "", false, fmt.Errorf("daemon ID cannot be empty")
	}
	var userID string
	var removed bool
	err := r.RunTxWithOptions(ctx, TxOptions{Isolation: IsolationReadCommitted}, func(txCtx context.Context) error {
		var err error
		userID, removed, err = r.removeDaemon(txCtx, daemonID)
		return err
	})
	if err != nil {
		return "", false, err
	}
	return userID, removed, nil
}

// removeDaemon deletes the row and its lease and releases its bindings. ctx
// must carry the caller's transaction (RunTx), so the release commits or rolls
// back with the removal.
func (r *Repo) removeDaemon(ctx context.Context, daemonID string) (string, bool, error) {
	if _, err := r.DB.ExecContext(ctx, r.bindQuery(`DELETE FROM daemon_attachment WHERE daemon_id = ?`), daemonID); err != nil {
		return "", false, fmt.Errorf("deleting attachment for removed daemon %s: %w", daemonID, err)
	}
	var userID string
	err := r.DB.QueryRowContext(ctx, r.bindQuery(`DELETE FROM daemons WHERE id = ? RETURNING user_id`), daemonID).Scan(&userID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("deleting daemon %s: %w", daemonID, err)
	}
	if err := r.releaseDaemonBindings(ctx, daemonID); err != nil {
		return "", false, err
	}
	return userID, true, nil
}

// releaseDaemonBindings drops every reference to a daemon that no longer
// exists. Three rows name a machine:
//
//   - chats.active_daemon_id, the chat's pin;
//   - worktrees.daemon_id, the machine holding a workspace's checkout;
//   - project_daemons, the project installed on a machine.
//
// Each resolves to a daemon nothing can reach once the machine is gone, so a
// chat on a deleted machine, or on one that came back under a new id, never
// recovered: its tools routed to the dead id forever, and
// validateOwnedProjectDaemon refused every OTHER machine for a project whose
// only install was on it. Released, each is re-learned from live evidence:
// the chat falls to its worktree's owner, else default resolution (exactly as
// a chat that never had a pin), a workspace is adopted by the machine that
// finds its directory (worktreesweep → AdoptWorktreeDaemon), and an install
// is re-recorded by the machine that has the checkout when it connects
// (reconcileProjectDaemons).
//
// Clearing a pin never sets no_machine: a chat on a machine stays on one.
// Open clients hear of the unpinned chat through the same
// chat_config_changed update SetChatDaemon sends.
func (r *Repo) releaseDaemonBindings(ctx context.Context, daemonID string) error {
	type unpinned struct {
		id, userID, projectID string
		worktreeID            sql.NullString
	}
	rows, err := r.DB.QueryContext(ctx, r.bindQuery(`
		UPDATE chats SET active_daemon_id = NULL, updated_at = NOW()
		WHERE active_daemon_id = ?
		RETURNING id, user_id, project_id, worktree_id
	`), daemonID)
	if err != nil {
		return fmt.Errorf("unpinning chats from removed daemon %s: %w", daemonID, err)
	}
	var chats []unpinned
	for rows.Next() {
		var c unpinned
		if err := rows.Scan(&c.id, &c.userID, &c.projectID, &c.worktreeID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scanning chat unpinned from removed daemon %s: %w", daemonID, err)
		}
		chats = append(chats, c)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("unpinning chats from removed daemon %s: %w", daemonID, err)
	}
	if _, err := r.DB.ExecContext(ctx, r.bindQuery(`UPDATE worktrees SET daemon_id = NULL WHERE daemon_id = ?`), daemonID); err != nil {
		return fmt.Errorf("releasing worktrees of removed daemon %s: %w", daemonID, err)
	}
	if _, err := r.DB.ExecContext(ctx, r.bindQuery(`DELETE FROM project_daemons WHERE daemon_id = ?`), daemonID); err != nil {
		return fmt.Errorf("deleting project installs on removed daemon %s: %w", daemonID, err)
	}

	for _, c := range chats {
		data, err := json.Marshal(map[string]interface{}{"chat_id": c.id, "active_daemon_id": ""})
		if err != nil {
			return err
		}
		update := &UserUpdate{
			UserID:     c.userID,
			ProjectID:  &c.projectID,
			ChatID:     &c.id,
			UpdateType: UserUpdateChatConfigChanged,
			EntityType: EntityTypeChat,
			EntityID:   c.id,
			Data:       data,
		}
		if c.worktreeID.Valid {
			update.WorktreeID = &c.worktreeID.String
		}
		if err := r.CreateUserUpdate(ctx, update); err != nil {
			return fmt.Errorf("announcing chat %s unpinned from removed daemon %s: %w", c.id, daemonID, err)
		}
	}
	return nil
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
	if snap.UserID == "" {
		return RegistrySnapshotResult{}, fmt.Errorf("snapshot user ID cannot be empty")
	}
	var result RegistrySnapshotResult
	err := r.RunTxWithOptions(ctx, TxOptions{Isolation: IsolationReadCommitted}, func(txCtx context.Context) error {
		var err error
		result, err = r.applyDaemonRegistrySnapshot(txCtx, snap)
		return err
	})
	if err != nil {
		return RegistrySnapshotResult{}, fmt.Errorf("registry snapshot for %s: %w", snap.UserID, err)
	}
	return result, nil
}

// applyDaemonRegistrySnapshot is one attempt at the snapshot, inside the
// transaction ctx carries. It builds its result from scratch, so a retried
// attempt reports only what the committed one did.
func (r *Repo) applyDaemonRegistrySnapshot(ctx context.Context, snap RegistrySnapshotApply) (RegistrySnapshotResult, error) {
	var result RegistrySnapshotResult
	keep := make([]string, 0, len(snap.Daemons))
	for _, d := range snap.Daemons {
		ident := d.Identity
		ident.UserID = snap.UserID
		keep = append(keep, ident.DaemonID)

		wrote, err := r.upsertDaemonIdentity(ctx, r.DB, ident)
		if err != nil {
			return RegistrySnapshotResult{}, err
		}
		if d.Lifecycle != nil {
			lc := *d.Lifecycle
			lc.DaemonID = ident.DaemonID
			applied, err := r.applyDaemonLifecycle(ctx, r.DB, lc)
			if err != nil {
				return RegistrySnapshotResult{}, err
			}
			wrote = wrote || applied
		}
		if wrote {
			result.Upserted = append(result.Upserted, ident.DaemonID)
		}
	}

	rows, err := r.DB.QueryContext(ctx, r.bindQuery(`
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
		if _, removed, err := r.removeDaemon(ctx, id); err != nil {
			return RegistrySnapshotResult{}, err
		} else if removed {
			result.Removed = append(result.Removed, id)
		}
	}
	return result, nil
}
