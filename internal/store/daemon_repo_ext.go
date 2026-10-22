// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Daemon. The generated daemon_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: daemonRepo (a *crud.Repo[Daemon]) and the
//     package-level GetDaemonByID / ListDaemon / CreateDaemon / UpdateDaemon / DeleteDaemon
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Daemon struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindDaemonBy(ctx context.Context, db orm.Context, /* args */) ([]*Daemon, error) {
//		var rows []*Daemon
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveDaemons(ctx context.Context, db orm.Context) ([]*Daemon, error) {
//		return ListDaemon(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// DaemonExtra carries fields on Daemon that are NOT columns — computed
// values, transient state, anything derived rather than stored. Daemon is
// embedded on DaemonExtra (see daemon_orm.go), so a field added here is a field on
// every Daemon value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type DaemonExtra struct {
}

// UpsertDaemonRegistration records a daemon's own registration (hostname,
// platform, capabilities, project paths, type), keeping a stored value the
// registration leaves nil. It reports whether a row was written: the
// conflict branch only fires for the row's own owner, so a registration
// naming another user's daemon writes nothing — in one statement, with no
// read-then-write window.
func UpsertDaemonRegistration(ctx context.Context, db orm.Context, d *core.Daemon, createdAt, now time.Time) (bool, error) {
	res, err := db.Bun().NewRaw(`
		INSERT INTO daemons (
			id, user_id, hostname, platform, capabilities, project_paths, daemon_type, created_at, updated_at
		) VALUES (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)
		ON CONFLICT(id) DO UPDATE SET
			hostname = COALESCE(excluded.hostname, daemons.hostname),
			platform = COALESCE(excluded.platform, daemons.platform),
			capabilities = COALESCE(excluded.capabilities, daemons.capabilities),
			project_paths = COALESCE(excluded.project_paths, daemons.project_paths),
			daemon_type = COALESCE(excluded.daemon_type, daemons.daemon_type),
			updated_at = excluded.updated_at
		WHERE daemons.user_id = excluded.user_id`,
		d.ID, d.UserID, d.Hostname, d.Platform, d.Capabilities, d.ProjectPaths, d.DaemonType, createdAt, now,
	).Exec(ctx)
	return rowsWritten(res, err)
}

// UpsertDaemonIdentity creates the registry row for a daemon the control
// plane reports, or brings the name on an existing row up to date; it reports
// whether a row was written. user_id is never rewritten, daemon_type is
// filled only when the row has none, and an unchanged daemon is a no-op
// rather than a write (the snapshot re-asserts every daemon on every sweep).
// An empty daemonType is NULL.
func UpsertDaemonIdentity(ctx context.Context, db orm.Context, id core.DaemonIdentity, createdAt, now time.Time) (bool, error) {
	var daemonType *string
	if id.DaemonType != "" {
		daemonType = &id.DaemonType
	}
	res, err := db.Bun().NewRaw(`
		INSERT INTO daemons (id, user_id, name, daemon_type, created_at, updated_at)
		VALUES (?0, ?1, ?2, ?3, ?4, ?5)
		ON CONFLICT (id) DO UPDATE SET
			name = COALESCE(NULLIF(excluded.name, ''), daemons.name),
			daemon_type = COALESCE(daemons.daemon_type, excluded.daemon_type),
			updated_at = excluded.updated_at
		WHERE (excluded.name <> '' AND daemons.name IS DISTINCT FROM excluded.name)
		   OR (daemons.daemon_type IS NULL AND excluded.daemon_type IS NOT NULL)`,
		id.DaemonID, id.UserID, id.Name, daemonType, createdAt, now,
	).Exec(ctx)
	return rowsWritten(res, err)
}

// GetDaemon returns the daemon. A missing one is svcerr.NotFound, and
// errors.Is(err, sql.ErrNoRows), as the Repository always reported it.
func GetDaemon(ctx context.Context, db orm.Context, id string) (*core.Daemon, error) {
	row, err := GetDaemonByID(ctx, db, id)
	if err != nil {
		return nil, err
	}
	return daemonToCore(row), nil
}

// ListDaemonsByUserID returns the user's daemons, most recently updated
// first.
func ListDaemonsByUserID(ctx context.Context, db orm.Context, userID string) ([]*core.Daemon, error) {
	var rows []*Daemon
	err := db.Bun().NewSelect().Model(&rows).
		Where("user_id = ?", userID).
		OrderExpr("updated_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*core.Daemon, len(rows))
	for i, row := range rows {
		out[i] = daemonToCore(row)
	}
	return out, nil
}

// ApplyDaemonLifecycle mirrors one control-plane lifecycle event onto an
// existing daemons row and reports whether it applied. last_status_changed_at
// is compared, not just written: lifecycle events are newest-wins with no
// ordering guarantee, so an event older than the stored one is dropped here,
// in SQL, across every replica and retry. A lifecycle event that omits size
// or OOM data is reporting a phase change, not clearing them.
func ApplyDaemonLifecycle(ctx context.Context, db orm.Context, lc core.DaemonLifecycleUpdate) (bool, error) {
	res, err := db.Bun().NewRaw(`
		UPDATE daemons
		SET
			lifecycle_phase = ?0,
			size = COALESCE(NULLIF(?1, ''), size),
			last_status_message = ?2,
			last_status_changed_at = ?3,
			last_oom_killed_at = COALESCE(?4, last_oom_killed_at),
			oom_kill_count = GREATEST(oom_kill_count, ?5),
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?6
		  AND (lifecycle_phase IS NULL OR last_status_changed_at IS NULL OR last_status_changed_at < ?3)`,
		lc.Phase, lc.Size, lc.StatusMessage, lc.ChangedAt, lc.LastOOMKilledAt, lc.OOMKillCount, lc.DaemonID,
	).Exec(ctx)
	return rowsWritten(res, err)
}

// SetDaemonLocalModels replaces the daemon's stored local model inventory.
func SetDaemonLocalModels(ctx context.Context, db orm.Context, daemonID, inventoryJSON string) error {
	_, err := db.Bun().NewUpdate().Model((*Daemon)(nil)).
		Set("local_models = ?", inventoryJSON).
		Where("id = ?", daemonID).
		Exec(ctx)
	return err
}

// ListDaemonLocalModels returns the stored local model inventory of every
// daemon the user owns that has published one, keyed by daemon ID.
func ListDaemonLocalModels(ctx context.Context, db orm.Context, userID string) (map[string]string, error) {
	var rows []*Daemon
	err := db.Bun().NewSelect().Model(&rows).
		Column(DaemonFieldID, DaemonFieldLocalModels).
		Where("user_id = ?", userID).
		Where("local_models <> ''").
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.LocalModels
	}
	return out, nil
}

// SetDaemonStorageState replaces the daemon's stored disk state.
func SetDaemonStorageState(ctx context.Context, db orm.Context, daemonID, stateJSON string) error {
	_, err := db.Bun().NewUpdate().Model((*Daemon)(nil)).
		Set("storage_state = ?", stateJSON).
		Where("id = ?", daemonID).
		Exec(ctx)
	return err
}

// GetDaemonStorageState returns the daemon's stored disk state; "" when it
// has none or there is no such daemon.
func GetDaemonStorageState(ctx context.Context, db orm.Context, daemonID string) (string, error) {
	row, err := GetDaemonByID(ctx, db, daemonID)
	if errors.Is(err, orm.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return row.StorageState, nil
}

// ListDaemonStorageState returns the stored disk state of every daemon the
// user owns that has reported one, keyed by daemon ID.
func ListDaemonStorageState(ctx context.Context, db orm.Context, userID string) (map[string]string, error) {
	var rows []*Daemon
	err := db.Bun().NewSelect().Model(&rows).
		Column(DaemonFieldID, DaemonFieldStorageState).
		Where("user_id = ?", userID).
		Where("storage_state <> ''").
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.StorageState
	}
	return out, nil
}

// DaemonLastSeenAt reports the last time a gateway heard from the user's
// daemon: its lease's last_stream_activity while one exists, else the
// last_seen_at its most recent released lease left behind. Zero when
// neither exists, or the daemon is not the user's. Bound ($N): a tool call
// deciding whether to wait for a daemon reads it.
func DaemonLastSeenAt(ctx context.Context, db orm.Context, userID, daemonID string) (time.Time, error) {
	var seen sql.NullTime
	err := db.QueryRow(ctx, `
		SELECT GREATEST(d.last_seen_at, a.last_stream_activity)
		FROM daemons d
		LEFT JOIN daemon_attachment a ON a.daemon_id = d.id AND a.user_id = d.user_id
		WHERE d.id = $1 AND d.user_id = $2`,
		daemonID, userID,
	).Scan(&seen)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !seen.Valid) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return seen.Time, nil
}

// DeleteDaemonRow deletes the daemon's lease and registry row and returns the
// owner the row belonged to; removed is false when there was no row.
func DeleteDaemonRow(ctx context.Context, db orm.Context, daemonID string) (userID string, removed bool, err error) {
	if _, err := db.Bun().NewDelete().Model((*DaemonAttachment)(nil)).Where("daemon_id = ?", daemonID).Exec(ctx); err != nil {
		return "", false, fmt.Errorf("deleting attachment for removed daemon %s: %w", daemonID, err)
	}
	var owners []string
	if _, err := db.Bun().NewDelete().Model((*Daemon)(nil)).
		Where("id = ?", daemonID).
		Returning("user_id").
		Exec(ctx, &owners); err != nil {
		return "", false, fmt.Errorf("deleting daemon %s: %w", daemonID, err)
	}
	if len(owners) == 0 {
		return "", false, nil
	}
	return owners[0], true, nil
}

// ReleaseDaemonFromWorkspaces drops the two references to a removed daemon
// this lane's tables hold: worktrees.daemon_id (the machine holding a
// workspace's checkout), re-learned when a machine finds the directory, and
// the project_daemons installs on it, re-recorded when the machine that has
// the checkout connects.
func ReleaseDaemonFromWorkspaces(ctx context.Context, db orm.Context, daemonID string) error {
	if _, err := db.Bun().NewUpdate().Model((*Worktree)(nil)).
		Set("daemon_id = NULL").
		Where("daemon_id = ?", daemonID).
		Exec(ctx); err != nil {
		return fmt.Errorf("releasing worktrees of removed daemon %s: %w", daemonID, err)
	}
	if _, err := db.Bun().NewDelete().Model((*ProjectDaemon)(nil)).Where("daemon_id = ?", daemonID).Exec(ctx); err != nil {
		return fmt.Errorf("deleting project installs on removed daemon %s: %w", daemonID, err)
	}
	return nil
}

// LockDaemonsAbsentFromSnapshot locks and returns the user's daemon rows that
// a registry snapshot does not keep: not in keep, created before
// keepCreatedAfter, and with no lease fresher than keepAttachedSince.
func LockDaemonsAbsentFromSnapshot(ctx context.Context, db orm.Context, userID string, keep []string, keepCreatedAfter, keepAttachedSince time.Time) ([]string, error) {
	var ids []string
	err := db.Bun().NewRaw(`
		SELECT d.id FROM daemons d
		WHERE d.user_id = ?0
		  AND NOT (d.id = ANY(COALESCE(?1::text[], ARRAY[]::text[])))
		  AND d.created_at < ?2
		  AND NOT EXISTS (
			SELECT 1 FROM daemon_attachment a
			WHERE a.daemon_id = d.id AND a.last_stream_activity > ?3
		  )
		FOR UPDATE OF d`,
		userID, pgdialect.Array(keep), keepCreatedAfter, keepAttachedSince,
	).Scan(ctx, &ids)
	if err != nil {
		return nil, fmt.Errorf("listing registry rows absent from snapshot: %w", err)
	}
	return ids, nil
}

func daemonToCore(row *Daemon) *core.Daemon {
	return &core.Daemon{
		ID:                  row.ID,
		UserID:              row.UserID,
		Hostname:            row.Hostname,
		Platform:            row.Platform,
		Capabilities:        row.Capabilities,
		ProjectPaths:        row.ProjectPaths,
		DaemonType:          row.DaemonType,
		Name:                row.Name,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
		LifecyclePhase:      row.LifecyclePhase,
		Size:                row.Size,
		LastStatusMessage:   row.LastStatusMessage,
		LastStatusChangedAt: row.LastStatusChangedAt,
		LastOOMKilledAt:     row.LastOomKilledAt,
		OOMKillCount:        row.OomKillCount,
	}
}

// rowsWritten reports whether a write touched a row.
func rowsWritten(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
