// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ProjectDaemonDetachment. The generated project_daemon_detachment_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: projectDaemonDetachmentRepo (a *crud.Repo[ProjectDaemonDetachment]) and the
//     package-level GetProjectDaemonDetachmentByPK / ListProjectDaemonDetachment / CreateProjectDaemonDetachment / UpsertProjectDaemonDetachment, keyed by
//     the whole composite key (project_id, daemon_id), plus UpdateProjectDaemonDetachment / DeleteProjectDaemonDetachmentByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ProjectDaemonDetachment struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindProjectDaemonDetachmentBy(ctx context.Context, db orm.Context, /* args */) ([]*ProjectDaemonDetachment, error) {
//		var rows []*ProjectDaemonDetachment
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("path = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveProjectDaemonDetachments(ctx context.Context, db orm.Context) ([]*ProjectDaemonDetachment, error) {
//		return ListProjectDaemonDetachment(ctx, db, orm.WhereEq("path", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// LockMachineCheckouts takes the transaction-scoped advisory lock that makes
// a detach from a machine and the resolution of a path that machine reports
// (its checkout, its detachments, a project created for the path) happen one
// after the other, never interleaved. ctx must carry a transaction.
func LockMachineCheckouts(ctx context.Context, db orm.Context, daemonID string) error {
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "machine-checkouts:"+daemonID); err != nil {
		return fmt.Errorf("failed to lock the machine's checkouts: %w", err)
	}
	return nil
}

// LockProjectCheckout takes the transaction-scoped advisory lock on the
// project's checkout on a machine: exclusive for an action that forgets the
// checkout (a detach), shared for the insert of a branch workspace that will
// be a git worktree of it. So a workspace either committed before the
// detach's guard read the workspaces, or is inserted after the detach
// committed, never in between. ctx must carry a
// transaction.
func LockProjectCheckout(ctx context.Context, db orm.Context, projectID, daemonID string, shared bool) error {
	fn := "pg_advisory_xact_lock"
	if shared {
		fn = "pg_advisory_xact_lock_shared"
	}
	if _, err := db.Exec(ctx, `SELECT `+fn+`(hashtextextended($1, 0))`, "project-checkout:"+projectID+":"+daemonID); err != nil {
		return fmt.Errorf("failed to lock the project's checkout on the machine: %w", err)
	}
	return nil
}

// RecordProjectDaemonDetachment records that the user detached the project
// from the machine, at the checkout path it had there. A second detach of the
// same pair moves its path and time.
func RecordProjectDaemonDetachment(ctx context.Context, db orm.Context, projectID, daemonID, path string) error {
	row := &ProjectDaemonDetachment{ProjectID: projectID, DaemonID: daemonID, Path: path}
	_, err := db.Bun().NewInsert().Model(row).
		Column(ProjectDaemonDetachmentFieldProjectID, ProjectDaemonDetachmentFieldDaemonID, ProjectDaemonDetachmentFieldPath).
		On("CONFLICT (project_id, daemon_id) DO UPDATE").
		Set("path = EXCLUDED.path").
		Set("detached_at = CURRENT_TIMESTAMP").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to record the project's detachment from the machine: %w", err)
	}
	return nil
}

// ClearProjectDaemonDetachment deletes the pair's detachment; none is not an
// error.
func ClearProjectDaemonDetachment(ctx context.Context, db orm.Context, projectID, daemonID string) error {
	if err := DeleteProjectDaemonDetachmentByPK(ctx, db, projectID, daemonID); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return fmt.Errorf("failed to clear the project's detachment from the machine: %w", err)
	}
	return nil
}

// ProjectDaemonDetached reports whether the user detached the project from
// the machine.
func ProjectDaemonDetached(ctx context.Context, db orm.Context, projectID, daemonID string) (bool, error) {
	detached, err := db.Bun().NewSelect().Model((*ProjectDaemonDetachment)(nil)).
		Where("project_id = ? AND daemon_id = ?", projectID, daemonID).
		Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to read the project's detachment from the machine: %w", err)
	}
	return detached, nil
}

// ListProjectDaemonDetachmentsForDaemon returns every project detached from
// the machine.
func ListProjectDaemonDetachmentsForDaemon(ctx context.Context, db orm.Context, daemonID string) ([]*core.ProjectDaemonDetachment, error) {
	rows, err := ListProjectDaemonDetachment(ctx, db, orm.WhereEq(ProjectDaemonDetachmentFieldDaemonID, daemonID))
	if err != nil {
		return nil, fmt.Errorf("failed to list the machine's detached projects: %w", err)
	}
	out := make([]*core.ProjectDaemonDetachment, len(rows))
	for i, row := range rows {
		out[i] = &core.ProjectDaemonDetachment{
			ProjectID:  row.ProjectID,
			DaemonID:   row.DaemonID,
			Path:       row.Path,
			DetachedAt: row.DetachedAt,
		}
	}
	return out, nil
}

// ProjectDaemonDetachmentExtra carries fields on ProjectDaemonDetachment that are NOT columns — computed
// values, transient state, anything derived rather than stored. ProjectDaemonDetachment is
// embedded on ProjectDaemonDetachmentExtra (see project_daemon_detachment_orm.go), so a field added here is a field on
// every ProjectDaemonDetachment value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ProjectDaemonDetachmentExtra struct {
}

// detachTx is the transaction every checkout lock writer runs in: READ COMMITTED, because the
// advisory locks taken inside are what serialize the writers, re-run on a deadlock between them
// and on the conflicts a body that creates rows can hit (a unique violation from a racing
// create, a swallowed statement error, a commit that rolled back).
var detachTx = orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts).WithRetryOn(func(err error) bool {
	return orm.RetryOnSQLState("40002", "25P02", "23505")(err) || orm.RetryOnCommitRollback(err)
})

// DetachProjectFromDaemon deletes the project's checkout record on the
// machine and that machine's synced config of the project, and records the
// detachment at path (the checkout path it had there), in one transaction.
// The project's config union is rewritten without the machine's catalog,
// under the same advisory lock every catalog writer takes
// (SyncProjectConfigSnapshot) and every checkout writer takes
// (UpsertProjectDaemon, RecordObservedProjectDaemonInScope). Nothing is sent to the
// machine.
//
// guard, when not nil, runs first inside that transaction, after the lock:
// the caller's preconditions (another checkout remains, no workspaces live
// there) are judged against every detach and checkout write that committed
// before it, so two concurrent detaches cannot each see the other's checkout
// and leave none. A guard error aborts the detach and is returned as is;
// core.ErrNothingToDetach aborts it as a success.
func DetachProjectFromDaemon(ctx context.Context, db orm.Context, projectID, daemonID, path string, guard func(ctx context.Context) error) error {
	if projectID == "" || daemonID == "" {
		return fmt.Errorf("project ID and daemon ID cannot be empty")
	}
	err := db.RunTx(ctx, detachTx, func(txCtx context.Context) error {
		if err := lockCheckoutForDetach(txCtx, db, projectID, daemonID); err != nil {
			return err
		}
		if guard != nil {
			if err := guard(txCtx); err != nil {
				return err
			}
		}
		if err := RecordProjectDaemonDetachment(txCtx, db, projectID, daemonID, path); err != nil {
			return err
		}
		return removeCheckoutLocked(txCtx, db, projectID, daemonID)
	})
	if errors.Is(err, core.ErrNothingToDetach) {
		return nil
	}
	return err
}

// DetachProjectDaemonOfOtherOrg is the machine-org cleanup of one checkout:
// when the project is in ANOTHER org than machineOrg, the machine's, right
// now, it detaches the project from the machine as DetachProjectFromDaemon
// does — the project_daemons row, the machine's synced config of the project
// (project_machine_catalogs, which the project's config union is rewritten
// without), under the same locks, through the same removal
// (removeCheckoutLocked) — after recording both rows whole in
// project_daemon_org_cleanups, so dev-docs/machine-org-rollout.md can put
// them back. It reports whether a checkout went.
//
// Two things differ from a user's detach, on purpose. It records no
// detachment: the platform, not the user, removed the record, and a project
// moved back into the machine's org is the machine's to find again. And the
// org is decided here, not from the caller's earlier read: the project row is
// locked FOR SHARE (a project's org move takes it FOR NO KEY UPDATE, and none
// of the detach's advisory locks, so the order cannot deadlock it) and its
// org re-read. A project now in the machine's org keeps its checkout, and so
// does one with no org yet (it is its owner's default org's, lazily; only a
// stamped org is authoritative enough to delete over). False (gone already,
// or no longer of another org) is not an error, so the cleanup is idempotent.
func DetachProjectDaemonOfOtherOrg(ctx context.Context, db orm.Context, projectID, daemonID, userID, machineOrg string) (bool, error) {
	if projectID == "" || daemonID == "" || machineOrg == "" {
		return false, fmt.Errorf("project, daemon and machine org are required")
	}
	detached := false
	err := db.RunTx(ctx, detachTx, func(txCtx context.Context) error {
		detached = false
		if err := lockCheckoutForDetach(txCtx, db, projectID, daemonID); err != nil {
			return err
		}
		// A project scheduled for deletion is detached all the same: the
		// cleanup removes, never adds, and the audit keeps what it removed
		// for a restore of either.
		org, _, found, err := LockProjectOrgForShare(txCtx, db, projectID)
		if err != nil || !found || org == nil || *org == machineOrg {
			return err
		}
		recorded, err := RecordProjectDaemonOrgCleanup(txCtx, db, projectID, daemonID, userID, machineOrg, org)
		if err != nil || !recorded {
			return err
		}
		if err := removeCheckoutLocked(txCtx, db, projectID, daemonID); err != nil {
			return err
		}
		detached = true
		return nil
	})
	return detached, err
}

// lockCheckoutForDetach takes, in order, every lock a detach of the
// project's checkout on the machine holds. ctx must carry the transaction.
func lockCheckoutForDetach(ctx context.Context, db orm.Context, projectID, daemonID string) error {
	if err := LockProjectCatalog(ctx, db, projectID); err != nil {
		return err
	}
	// Then the machine's: a path the machine reports is resolved under it
	// (WithMachineCheckoutsLocked), so that resolution sees this detach
	// whole or not at all. Always taken second, after the catalog lock; the
	// resolution takes no catalog lock while it holds this one.
	if err := LockMachineCheckouts(ctx, db, daemonID); err != nil {
		return err
	}
	// Then the checkout's, exclusive: a branch workspace of it is inserted
	// under the shared side (CreateWorktree), so a guard sees every one that
	// committed and none lands until the detach has.
	return LockProjectCheckout(ctx, db, projectID, daemonID, false)
}

// removeCheckoutLocked is what detaching a project from a machine removes:
// the checkout record, the machine's synced config of the project, and that
// config's share of the project's config union, which is rewritten from the
// machines left. ctx must carry the transaction holding
// lockCheckoutForDetach's locks.
func removeCheckoutLocked(ctx context.Context, db orm.Context, projectID, daemonID string) error {
	if err := RemoveProjectDaemon(ctx, db, projectID, daemonID); err != nil {
		return err
	}
	if err := DeleteProjectMachineCatalog(ctx, db, projectID, daemonID); err != nil {
		return err
	}
	return rewriteProjectCatalogUnion(ctx, db, projectID)
}

// rewriteProjectCatalogUnion recomputes the union columns of the project's
// config record from the machines' catalogs as they are now. ctx must carry
// the transaction holding LockProjectCatalog.
func rewriteProjectCatalogUnion(ctx context.Context, db orm.Context, projectID string) error {
	catalogs, err := ProjectMachineCatalogs(ctx, db, projectID)
	if err != nil {
		return err
	}
	union, err := ProjectFilesUnion(ctx, db, projectID)
	if err != nil {
		return err
	}
	if union.WorkflowsJSON, err = config.EncodeCatalogJSON(config.UnionWorkflows(catalogs)); err != nil {
		return err
	}
	if union.PresetsJSON, err = config.EncodeCatalogJSON(config.UnionPresets(catalogs)); err != nil {
		return err
	}
	return SetProjectConfigCatalogUnion(ctx, db, projectID, union, time.Now().UTC())
}

// WithMachineCheckoutsLocked runs fn in a transaction holding the machine's
// checkout lock, the one DetachProjectFromDaemon takes: every read and write
// fn makes with the ctx it is handed joins that transaction, so a detach from
// the machine commits wholly before fn's reads or after its writes. fn must
// not take a project's catalog lock (the detach takes that one first). It may
// run more than once, on a deadlock retry.
func WithMachineCheckoutsLocked(ctx context.Context, db orm.Context, daemonID string, fn func(ctx context.Context) error) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	return db.RunTx(ctx, detachTx, func(txCtx context.Context) error {
		if err := LockMachineCheckouts(txCtx, db, daemonID); err != nil {
			return err
		}
		return fn(txCtx)
	})
}
