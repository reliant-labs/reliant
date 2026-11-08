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

	"github.com/reliant-labs/forge/pkg/orm"

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
// project's checkout on a machine: exclusive for an action that destroys or
// forgets the checkout (remove, reclone, detach), shared for the insert of a
// branch workspace that will be a git worktree of it. So a workspace either
// committed before the destroyer's guard read the workspaces, or is inserted
// after the destroyer finished, never in between. ctx must carry a
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

// SetProjectCheckoutTeardownPending stamps the checkout's
// teardown_pending_at (pending) or clears it: set before a remove or reclone
// is sent, cleared once its outcome is known. A missing row is not an error.
func SetProjectCheckoutTeardownPending(ctx context.Context, db orm.Context, projectID, daemonID string, pending bool) error {
	set := "teardown_pending_at = NULL"
	if pending {
		set = "teardown_pending_at = NOW()"
	}
	if _, err := db.Bun().NewUpdate().Model((*ProjectDaemon)(nil)).
		Set(set).
		Where("project_id = ?", projectID).
		Where("daemon_id = ?", daemonID).
		Exec(ctx); err != nil {
		return fmt.Errorf("failed to record the checkout's pending teardown: %w", err)
	}
	return nil
}

// ProjectCheckoutTeardownPending reports whether a remove or reclone of the
// project's checkout on the machine was sent and its outcome never heard.
func ProjectCheckoutTeardownPending(ctx context.Context, db orm.Context, projectID, daemonID string) (bool, error) {
	pending, err := db.Bun().NewSelect().Model((*ProjectDaemon)(nil)).
		Where("project_id = ?", projectID).
		Where("daemon_id = ?", daemonID).
		Where("teardown_pending_at IS NOT NULL").
		Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to read the checkout's pending teardown: %w", err)
	}
	return pending, nil
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
