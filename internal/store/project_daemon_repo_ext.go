// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ProjectDaemon. The generated project_daemon_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: projectDaemonRepo (a *crud.Repo[ProjectDaemon]) and the
//     package-level GetProjectDaemonByPK / ListProjectDaemon / CreateProjectDaemon / UpsertProjectDaemon, keyed by
//     the whole composite key (project_id, daemon_id), plus UpdateProjectDaemon / DeleteProjectDaemonByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ProjectDaemon struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindProjectDaemonBy(ctx context.Context, db orm.Context, /* args */) ([]*ProjectDaemon, error) {
//		var rows []*ProjectDaemon
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("path = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveProjectDaemons(ctx context.Context, db orm.Context) ([]*ProjectDaemon, error) {
//		return ListProjectDaemon(ctx, db, orm.WhereEq("path", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ProjectDaemonExtra carries fields on ProjectDaemon that are NOT columns — computed
// values, transient state, anything derived rather than stored. ProjectDaemon is
// embedded on ProjectDaemonExtra (see project_daemon_orm.go), so a field added here is a field on
// every ProjectDaemon value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ProjectDaemonExtra struct {
}

// RecordProjectDaemonInstalled records a COMPLETED clone, or a checkout the
// connect-time reconcile just found on the daemon's disk: either way it is
// there now, so verified_at is stamped (machine-pool placement reads it).
// install_state is forced to installed rather than left to the column
// default, so a row that was installing or failed is corrected when the clone
// finally lands. An
// existing row keeps its cloned_at, which is why this is not the generated
// UpsertProjectDaemon (that rewrites every non-key column).
func RecordProjectDaemonInstalled(ctx context.Context, db orm.Context, projectID, daemonID, path string, defaultBranch *string) error {
	row := &ProjectDaemon{
		ProjectID: projectID, DaemonID: daemonID, Path: path, DefaultBranch: defaultBranch,
		InstallState: core.ProjectInstallInstalled,
	}
	_, err := db.Bun().NewInsert().Model(row).
		Column(ProjectDaemonFieldProjectID, ProjectDaemonFieldDaemonID, ProjectDaemonFieldPath,
			ProjectDaemonFieldDefaultBranch, ProjectDaemonFieldInstallState, ProjectDaemonFieldVerifiedAt).
		Value(ProjectDaemonFieldVerifiedAt, "NOW()").
		On("CONFLICT (project_id, daemon_id) DO UPDATE").
		Set("path = EXCLUDED.path").
		Set("default_branch = EXCLUDED.default_branch").
		Set("install_state = EXCLUDED.install_state").
		Set("install_error = ''").
		Set("install_request_id = ''").
		Set("verified_at = EXCLUDED.verified_at").
		Exec(ctx)
	return err
}

// RecordProjectDaemonQueued records a clone that has been QUEUED for a
// daemon but has not run. The path is where the checkout WILL be, not where
// it is. requestID ties the row to the queued command so the outcome can find
// it.
func RecordProjectDaemonQueued(ctx context.Context, db orm.Context, projectID, daemonID, path string, defaultBranch *string, requestID string) error {
	row := &ProjectDaemon{
		ProjectID: projectID, DaemonID: daemonID, Path: path, DefaultBranch: defaultBranch,
		InstallState: core.ProjectInstallInstalling, InstallRequestID: requestID,
	}
	_, err := db.Bun().NewInsert().Model(row).
		Column(ProjectDaemonFieldProjectID, ProjectDaemonFieldDaemonID, ProjectDaemonFieldPath,
			ProjectDaemonFieldDefaultBranch, ProjectDaemonFieldInstallState,
			ProjectDaemonFieldInstallError, ProjectDaemonFieldInstallRequestID).
		On("CONFLICT (project_id, daemon_id) DO UPDATE").
		Set("path = EXCLUDED.path").
		Set("default_branch = EXCLUDED.default_branch").
		Set("install_state = EXCLUDED.install_state").
		Set("install_error = ''").
		Set("install_request_id = EXCLUDED.install_request_id").
		Exec(ctx)
	return err
}

// ResolveQueuedProjectDaemon records the outcome the daemon reported for a
// queued clone. Matched on request id, because the notification carries no
// project id. A non-empty installErr marks the row failed; otherwise it is
// installed, as of now.
func ResolveQueuedProjectDaemon(ctx context.Context, db orm.Context, requestID, installErr string) error {
	q := db.Bun().NewUpdate().Model((*ProjectDaemon)(nil))
	if installErr != "" {
		q = q.Set("install_state = ?", core.ProjectInstallFailed).
			Set("install_error = ?", installErr)
	} else {
		q = q.Set("install_state = ?", core.ProjectInstallInstalled).
			Set("install_error = ''").
			Set("cloned_at = NOW()").
			Set("verified_at = NOW()")
	}
	_, err := q.Where("install_request_id = ?", requestID).
		Where("install_request_id <> ''").
		Exec(ctx)
	return err
}

// MarkProjectDaemonInstalled settles a queued clone from the daemon's
// filesystem announcement, which carries the path but no request id. A no-op
// for a row already installed.
func MarkProjectDaemonInstalled(ctx context.Context, db orm.Context, projectID, daemonID string) error {
	_, err := db.Bun().NewUpdate().Model((*ProjectDaemon)(nil)).
		Set("install_state = ?", core.ProjectInstallInstalled).
		Set("install_error = ''").
		Set("cloned_at = NOW()").
		Set("verified_at = NOW()").
		Where("project_id = ?", projectID).
		Where("daemon_id = ?", daemonID).
		Where("install_state <> ?", core.ProjectInstallInstalled).
		Exec(ctx)
	return err
}

// ListProjectDaemonsForProject returns every daemon the project is (or is
// being) installed on, oldest clone first.
func ListProjectDaemonsForProject(ctx context.Context, db orm.Context, projectID string) ([]*core.ProjectDaemon, error) {
	rows, err := ListProjectDaemon(ctx, db,
		orm.WhereEq(ProjectDaemonFieldProjectID, projectID),
		orm.WithOrderBy(ProjectDaemonFieldClonedAt, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list project_daemons for project: %w", err)
	}
	return projectDaemonsToCore(rows), nil
}

// ListProjectDaemonsForDaemon returns every project installed (or being
// installed) on the daemon, oldest clone first.
func ListProjectDaemonsForDaemon(ctx context.Context, db orm.Context, daemonID string) ([]*core.ProjectDaemon, error) {
	rows, err := ListProjectDaemon(ctx, db,
		orm.WhereEq(ProjectDaemonFieldDaemonID, daemonID),
		orm.WithOrderBy(ProjectDaemonFieldClonedAt, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list project_daemons for daemon: %w", err)
	}
	return projectDaemonsToCore(rows), nil
}

// RemoveProjectDaemon deletes the installation row; a missing one is not an
// error.
func RemoveProjectDaemon(ctx context.Context, db orm.Context, projectID, daemonID string) error {
	if err := DeleteProjectDaemonByPK(ctx, db, projectID, daemonID); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

func projectDaemonsToCore(rows []*ProjectDaemon) []*core.ProjectDaemon {
	out := make([]*core.ProjectDaemon, len(rows))
	for i, row := range rows {
		out[i] = &core.ProjectDaemon{
			ProjectID:        row.ProjectID,
			DaemonID:         row.DaemonID,
			Path:             row.Path,
			DefaultBranch:    row.DefaultBranch,
			ClonedAt:         row.ClonedAt,
			InstallState:     row.InstallState,
			InstallError:     row.InstallError,
			InstallRequestID: row.InstallRequestID,
		}
	}
	return out
}
