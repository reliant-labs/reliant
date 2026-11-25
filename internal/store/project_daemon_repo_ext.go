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
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

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
			ProjectDaemonFieldInstallError, ProjectDaemonFieldInstallRequestID, ProjectDaemonFieldCloneStartedAt).
		// The clone can start now only if its machine is connected now (a
		// fresh lease); one queued to a sleeping machine starts when it next
		// connects, and that connect's reconcile stamps it
		// (StartQueuedProjectDaemonClones).
		Value(ProjectDaemonFieldCloneStartedAt, `CASE WHEN EXISTS (
			SELECT 1 FROM daemon_attachment a
			WHERE a.daemon_id = ? AND a.last_stream_activity > now() - interval '90 seconds'
		) THEN now() END`, daemonID).
		On("CONFLICT (project_id, daemon_id) DO UPDATE").
		Set("path = EXCLUDED.path").
		Set("default_branch = EXCLUDED.default_branch").
		Set("install_state = EXCLUDED.install_state").
		Set("install_error = ''").
		Set("install_request_id = EXCLUDED.install_request_id").
		// An installing row's cloned_at is when its clone was queued (an
		// insert's default, and re-stamped here for a re-clone); a re-clone
		// starts its own clock too.
		Set("cloned_at = NOW()").
		Set("clone_started_at = EXCLUDED.clone_started_at").
		Exec(ctx)
	return err
}

// StartQueuedProjectDaemonClones stamps clone_started_at on the daemon's
// installing rows that have none: clones queued while it was away, which can
// run now that it has connected. The connect reconcile calls it before it
// reads the rows, so a clone's stuck-clone timeout counts from the connect
// that let it start, not from when it was queued.
func StartQueuedProjectDaemonClones(ctx context.Context, db orm.Context, daemonID string) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	_, err := db.Bun().NewUpdate().Model((*ProjectDaemon)(nil)).
		Set("clone_started_at = NOW()").
		Where("daemon_id = ?", daemonID).
		Where("install_state = ?", core.ProjectInstallInstalling).
		Where("clone_started_at IS NULL").
		Exec(ctx)
	return err
}

// FailProjectDaemonInstall settles a clone that is still installing as
// failed, with installErr as the reason: the connect reconcile's answer to a
// clone whose outcome was lost. A row that settled meanwhile is left alone,
// and install_request_id is kept so a late outcome still finds the row. It
// reports whether it marked the row failed.
func FailProjectDaemonInstall(ctx context.Context, db orm.Context, projectID, daemonID, installErr string) (bool, error) {
	res, err := db.Bun().NewUpdate().Model((*ProjectDaemon)(nil)).
		Set("install_state = ?", core.ProjectInstallFailed).
		Set("install_error = ?", installErr).
		Where("project_id = ?", projectID).
		Where("daemon_id = ?", daemonID).
		Where("install_state = ?", core.ProjectInstallInstalling).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
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

// ProjectDaemonExists reports whether the machine has a checkout record of
// the project, in any install state.
func ProjectDaemonExists(ctx context.Context, db orm.Context, projectID, daemonID string) (bool, error) {
	return db.Bun().NewSelect().Model((*ProjectDaemon)(nil)).
		Where("project_id = ?", projectID).
		Where("daemon_id = ?", daemonID).
		Exists(ctx)
}

// ProjectDaemonInstalled reports whether the project's checkout on the
// daemon is recorded as installed.
func ProjectDaemonInstalled(ctx context.Context, db orm.Context, projectID, daemonID string) (bool, error) {
	return db.Bun().NewSelect().Model((*ProjectDaemon)(nil)).
		Where("project_id = ?", projectID).
		Where("daemon_id = ?", daemonID).
		Where("install_state = ?", core.ProjectInstallInstalled).
		Exists(ctx)
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

// ListProjectDaemonsForProjects returns where every project in projectIDs is
// (or is being) installed, in one read, by project and then oldest clone
// first.
func ListProjectDaemonsForProjects(ctx context.Context, db orm.Context, projectIDs []string) ([]*core.ProjectDaemon, error) {
	if len(projectIDs) == 0 {
		return []*core.ProjectDaemon{}, nil
	}
	rows, err := ListProjectDaemon(ctx, db,
		orm.WhereIn(ProjectDaemonFieldProjectID, projectIDs),
		orm.WithOrderBy(ProjectDaemonFieldProjectID, orm.Asc),
		orm.WithOrderBy(ProjectDaemonFieldClonedAt, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list project_daemons for projects: %w", err)
	}
	return projectDaemonsToCore(rows), nil
}

// ListProjectDaemonsForDaemon returns every project installed (or being
// installed) on the daemon, oldest clone first.
func ListProjectDaemonsForDaemon(ctx context.Context, db orm.Context, daemonID string) ([]*core.ProjectDaemon, error) {
	if daemonID == "" {
		return nil, fmt.Errorf("daemon ID cannot be empty")
	}
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
			CloneStartedAt:   row.CloneStartedAt,
			VerifiedAt:       row.VerifiedAt,
		}
	}
	return out
}

// RecordProjectDaemonOrgCleanup records, in project_daemon_org_cleanups,
// the project's checkout on the machine that the machine-org cleanup is about
// to detach — the project_daemons row whole, and the machine's synced config
// of the project (its project_machine_catalogs row, whole; NULL when it has
// none) — so dev-docs/machine-org-rollout.md can put both back exactly. It
// reports whether there was a checkout to record. ctx must carry the
// cleanup's transaction (db.Repo.DetachProjectDaemonOfOtherOrg), which then
// removes what was recorded.
//
// The catalog row is recorded with to_jsonb of the whole row: it has no
// entity, and the restore is jsonb_populate_record of the table's own type,
// so every column — those this release does not name included — comes back.
func RecordProjectDaemonOrgCleanup(ctx context.Context, db orm.Context, projectID, daemonID, userID, machineOrg string, projectOrg *string) (bool, error) {
	var ids []int64
	err := db.Bun().NewRaw(`
INSERT INTO project_daemon_org_cleanups (project_id, daemon_id, user_id, path, machine_org_id, project_org_id, detached_row, detached_catalog)
SELECT pd.project_id, pd.daemon_id, ?2, pd.path, ?3, ?4,
       (SELECT to_jsonb(r) FROM (SELECT `+columnsOf[ProjectDaemon]("pd")+`) r),
       (SELECT to_jsonb(c) FROM project_machine_catalogs c WHERE c.project_id = pd.project_id AND c.daemon_id = pd.daemon_id)
FROM project_daemons pd
WHERE pd.project_id = ?0 AND pd.daemon_id = ?1
RETURNING id`, projectID, daemonID, userID, machineOrg, projectOrg).Scan(ctx, &ids)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return len(ids) > 0, nil
}

// LockProjectOrgForShare reads the project's org, and whether it is
// scheduled for deletion (projects.delete_after), with its row locked FOR
// SHARE: a concurrent org move (FOR NO KEY UPDATE) or deletion schedule (FOR
// UPDATE) is either committed and seen, or waits for the caller's
// transaction. found is false for a project that is gone. ctx must carry a
// transaction.
func LockProjectOrgForShare(ctx context.Context, db orm.Context, projectID string) (org *string, scheduled, found bool, err error) {
	var rows []struct {
		OrgID     *string `bun:"org_id"`
		Scheduled bool    `bun:"scheduled"`
	}
	if err := db.Bun().NewRaw(`SELECT org_id, delete_after IS NOT NULL AS scheduled FROM projects WHERE id = ? FOR SHARE`, projectID).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, false, fmt.Errorf("lock the project's org: %w", err)
	}
	if len(rows) == 0 {
		return nil, false, false, nil
	}
	return rows[0].OrgID, rows[0].Scheduled, true, nil
}

// ── Nested repositories of a checkout (project_daemon_repos) ───────────
//
// project_daemon_repos is forge:no-entity: its columns are named here, and
// every read joins repos for the repository's name, relative path and
// remote, which are the Repo row's and never copied.

const projectDaemonRepoSelect = `SELECT pdr.project_id, pdr.daemon_id, pdr.repo_id,
	r.name, r.relative_path, COALESCE(r.remote_url, '') AS remote_url,
	pdr.state, pdr.error, pdr.state_changed_at, pdr.lease_until
FROM project_daemon_repos pdr JOIN repos r ON r.id = pdr.repo_id`

type projectDaemonRepoRow struct {
	ProjectID      string     `bun:"project_id"`
	DaemonID       string     `bun:"daemon_id"`
	RepoID         string     `bun:"repo_id"`
	Name           string     `bun:"name"`
	RelativePath   string     `bun:"relative_path"`
	RemoteURL      string     `bun:"remote_url"`
	State          string     `bun:"state"`
	Error          string     `bun:"error"`
	StateChangedAt time.Time  `bun:"state_changed_at"`
	LeaseUntil     *time.Time `bun:"lease_until"`
}

func projectDaemonReposToCore(rows []projectDaemonRepoRow) []*core.ProjectDaemonRepo {
	out := make([]*core.ProjectDaemonRepo, len(rows))
	for i, r := range rows {
		out[i] = &core.ProjectDaemonRepo{
			ProjectID: r.ProjectID, DaemonID: r.DaemonID, RepoID: r.RepoID,
			Name: r.Name, RelativePath: r.RelativePath, RemoteURL: r.RemoteURL,
			State: core.NestedRepoState(r.State), Error: r.Error, StateChangedAt: r.StateChangedAt,
			LeaseUntil: r.LeaseUntil,
		}
	}
	return out
}

// PlanProjectDaemonRepos records the nested repositories to clone into the
// project's checkout on the machine, once its root clone lands: each of
// repoIDs pending (again, for one that cloned or failed before: a set-up
// that is run again clones every one again, and an existing clone is kept
// by the daemon), and every other row of the pair removed.
//
// A row whose claim's lease still holds keeps its claim and lease: the
// machine may still be running that clone, and resetting it would let a
// second pass send a clone of the same target while the first runs (the
// daemon's clone sweeps the target's staging directories). It is marked
// replan_pending instead, and goes back to pending when that claim settles
// (SettleProjectDaemonRepo), or is cleared by the claim that takes it over
// once the lease runs out. Every other row's claim is dropped. The pair's
// project_daemons row must exist. Run it in the transaction that queues the
// root clone.
func PlanProjectDaemonRepos(ctx context.Context, db orm.Context, projectID, daemonID string, repoIDs []string) error {
	if projectID == "" || daemonID == "" {
		return fmt.Errorf("project ID and daemon ID cannot be empty")
	}
	del := db.Bun().NewDelete().TableExpr("project_daemon_repos").
		Where("project_id = ?", projectID).Where("daemon_id = ?", daemonID)
	if len(repoIDs) > 0 {
		del = del.Where("repo_id NOT IN (?)", bun.List(repoIDs))
	}
	if _, err := del.Exec(ctx); err != nil {
		return fmt.Errorf("clear the machine's nested repositories: %w", err)
	}
	for _, repoID := range repoIDs {
		if _, err := db.Bun().NewRaw(`INSERT INTO project_daemon_repos (project_id, daemon_id, repo_id, state, error, state_changed_at)
VALUES (?0, ?1, ?2, 'pending', '', now())
ON CONFLICT (project_id, daemon_id, repo_id) DO UPDATE SET
	state = CASE WHEN `+liveClaim+` THEN 'cloning' ELSE 'pending' END,
	error = '',
	state_changed_at = CASE WHEN `+liveClaim+` THEN project_daemon_repos.state_changed_at ELSE now() END,
	claim_id = CASE WHEN `+liveClaim+` THEN project_daemon_repos.claim_id ELSE '' END,
	lease_until = CASE WHEN `+liveClaim+` THEN project_daemon_repos.lease_until ELSE NULL END,
	replan_pending = `+liveClaim,
			projectID, daemonID, repoID).Exec(ctx); err != nil {
			return fmt.Errorf("plan nested repository %s: %w", repoID, err)
		}
	}
	return nil
}

// liveClaim is true of a project_daemon_repos row (in an ON CONFLICT update)
// whose claim's lease still holds: the machine may still be running its clone.
const liveClaim = `(project_daemon_repos.state = 'cloning' AND project_daemon_repos.lease_until IS NOT NULL AND project_daemon_repos.lease_until >= now())`

type nestedRepoClaimRow struct {
	projectDaemonRepoRow
	InsideFailed bool `bun:"inside_failed"`
}

// ClaimNextProjectDaemonRepo claims, for the pass named by claimID, one
// nested repository of the project's checkout on the machine that is ready
// to clone, shallowest first: a pending one, or one whose claim's lease ran
// out (its pass went away, and the machine can no longer be running the
// clone it was sent). One sitting inside a repository of the checkout that
// is still pending or cloning is not ready: a clone needs its parent landed,
// and two clones of one tree never run at once. Its lease runs lease from
// the database's clock, which must cover the clone's own timeout on the
// machine. nil when none is ready. Concurrent passes never claim one row:
// the candidate is locked SKIP LOCKED and the update re-checks it.
func ClaimNextProjectDaemonRepo(ctx context.Context, db orm.Context, projectID, daemonID, claimID string, lease time.Duration) (*core.NestedRepoClaim, error) {
	if projectID == "" || daemonID == "" || claimID == "" {
		return nil, fmt.Errorf("project ID, daemon ID and claim ID cannot be empty")
	}
	var rows []nestedRepoClaimRow
	err := db.Bun().NewRaw(`WITH pair AS (
	SELECT p.repo_id, p.state, trim(both '/' from r.relative_path) AS rel
	FROM project_daemon_repos p JOIN repos r ON r.id = p.repo_id
	WHERE p.project_id = ?0 AND p.daemon_id = ?1
), candidate AS (
	SELECT c.repo_id
	FROM project_daemon_repos c JOIN repos cr ON cr.id = c.repo_id
	WHERE c.project_id = ?0 AND c.daemon_id = ?1
	  AND (c.state = 'pending' OR (c.state = 'cloning' AND (c.lease_until IS NULL OR c.lease_until < now())))
	  AND NOT EXISTS (
		SELECT 1 FROM pair a
		WHERE a.repo_id <> c.repo_id AND a.rel <> '' AND a.state IN ('pending', 'cloning')
		  AND starts_with(trim(both '/' from cr.relative_path), a.rel || '/'))
	ORDER BY length(trim(both '/' from cr.relative_path)) - length(replace(trim(both '/' from cr.relative_path), '/', '')),
		cr.relative_path
	LIMIT 1
	FOR UPDATE OF c SKIP LOCKED
), claimed AS (
	UPDATE project_daemon_repos u SET state = 'cloning', error = '', claim_id = ?2, replan_pending = false,
		lease_until = now() + make_interval(secs => ?3), state_changed_at = now()
	FROM candidate
	WHERE u.project_id = ?0 AND u.daemon_id = ?1 AND u.repo_id = candidate.repo_id
	  AND (u.state = 'pending' OR (u.state = 'cloning' AND (u.lease_until IS NULL OR u.lease_until < now())))
	RETURNING u.project_id, u.daemon_id, u.repo_id, u.state, u.error, u.state_changed_at, u.lease_until
)
SELECT c.project_id, c.daemon_id, c.repo_id, r.name, r.relative_path, COALESCE(r.remote_url, '') AS remote_url,
	c.state, c.error, c.state_changed_at, c.lease_until,
	EXISTS (SELECT 1 FROM pair a
		WHERE a.repo_id <> c.repo_id AND a.rel <> '' AND a.state = 'failed'
		  AND starts_with(trim(both '/' from r.relative_path), a.rel || '/')) AS inside_failed
FROM claimed c JOIN repos r ON r.id = c.repo_id`, projectID, daemonID, claimID, lease.Seconds()).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("claim a nested repository: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &core.NestedRepoClaim{
		Repo:         projectDaemonReposToCore([]projectDaemonRepoRow{rows[0].projectDaemonRepoRow})[0],
		InsideFailed: rows[0].InsideFailed,
	}, nil
}

// SettleProjectDaemonRepo records how the clone of a nested repository the
// pass claimID claimed went: installed when installErr is empty, else failed
// with it. It is fenced on the claim: a row no longer held by that claim
// (taken over after its lease ran out) is left alone, and settled reports
// false. A row planned again while this claim ran (replan_pending) goes back
// to pending instead, its outcome belonging to the previous plan: the new
// plan clones it again now that this clone has ended (a clone that landed is
// kept by the daemon). settled reports true then too: the claim is released.
func SettleProjectDaemonRepo(ctx context.Context, db orm.Context, projectID, daemonID, repoID, claimID, installErr string) (bool, error) {
	if claimID == "" {
		return false, fmt.Errorf("claim ID cannot be empty")
	}
	state := core.NestedRepoInstalled
	if installErr != "" {
		state = core.NestedRepoFailed
	}
	res, err := db.Bun().NewRaw(`UPDATE project_daemon_repos SET
	state = CASE WHEN replan_pending THEN 'pending' ELSE ?3 END,
	error = CASE WHEN replan_pending THEN '' ELSE ?4 END,
	state_changed_at = now(), claim_id = '', lease_until = NULL, replan_pending = false
WHERE project_id = ?0 AND daemon_id = ?1 AND repo_id = ?2 AND state = 'cloning' AND claim_id = ?5`,
		projectID, daemonID, repoID, string(state), installErr, claimID).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkProjectDaemonInstalledFromNestedRepos settles the checkout installed
// once its root has landed and every one of its nested repositories cloned:
// it changes nothing while one is still to clone or one failed, so it never
// reports a checkout missing a nested repository as installed. A checkout
// with no nested rows is settled like MarkProjectDaemonInstalled.
func MarkProjectDaemonInstalledFromNestedRepos(ctx context.Context, db orm.Context, projectID, daemonID string) error {
	_, err := db.Bun().NewRaw(`UPDATE project_daemons SET install_state = ?2, install_error = '', cloned_at = NOW(), verified_at = NOW()
WHERE project_id = ?0 AND daemon_id = ?1 AND install_state <> ?2
  AND NOT EXISTS (SELECT 1 FROM project_daemon_repos n
	WHERE n.project_id = ?0 AND n.daemon_id = ?1 AND n.state IN ('pending', 'cloning', 'failed'))`,
		projectID, daemonID, string(core.ProjectInstallInstalled)).Exec(ctx)
	return err
}

// FailProjectDaemonFromNestedRepos settles the checkout failed with
// installErr because a nested repository of its current plan did not
// clone: from installing, or from installed (an older gateway settled the
// root alone before this release cloned the nested ones). It is fenced on
// the plan: it changes nothing unless a nested row failed and none is still
// to clone, so a set-up planned again meanwhile is not failed by the old
// plan's outcome. It reports whether it marked the row failed.
func FailProjectDaemonFromNestedRepos(ctx context.Context, db orm.Context, projectID, daemonID, installErr string) (bool, error) {
	res, err := db.Bun().NewRaw(`UPDATE project_daemons SET install_state = ?2, install_error = ?3
WHERE project_id = ?0 AND daemon_id = ?1 AND install_state IN (?4, ?5)
  AND EXISTS (SELECT 1 FROM project_daemon_repos n
	WHERE n.project_id = ?0 AND n.daemon_id = ?1 AND n.state = 'failed')
  AND NOT EXISTS (SELECT 1 FROM project_daemon_repos n
	WHERE n.project_id = ?0 AND n.daemon_id = ?1 AND n.state IN ('pending', 'cloning'))`,
		projectID, daemonID, string(core.ProjectInstallFailed), installErr,
		string(core.ProjectInstallInstalling), string(core.ProjectInstallInstalled)).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ListProjectDaemonReposForPair returns the nested repositories of the
// project's checkout on the machine, by relative path.
func ListProjectDaemonReposForPair(ctx context.Context, db orm.Context, projectID, daemonID string) ([]*core.ProjectDaemonRepo, error) {
	var rows []projectDaemonRepoRow
	err := db.Bun().NewRaw(projectDaemonRepoSelect+`
WHERE pdr.project_id = ?0 AND pdr.daemon_id = ?1
ORDER BY r.relative_path`, projectID, daemonID).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("list nested repositories: %w", err)
	}
	return projectDaemonReposToCore(rows), nil
}

// ListProjectDaemonReposForProjects returns the nested repositories of
// every checkout of the projects, by project, machine and relative path.
func ListProjectDaemonReposForProjects(ctx context.Context, db orm.Context, projectIDs []string) ([]*core.ProjectDaemonRepo, error) {
	if len(projectIDs) == 0 {
		return []*core.ProjectDaemonRepo{}, nil
	}
	var rows []projectDaemonRepoRow
	err := db.Bun().NewRaw(projectDaemonRepoSelect+`
WHERE pdr.project_id IN (?)
ORDER BY pdr.project_id, pdr.daemon_id, r.relative_path`, bun.List(projectIDs)).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("list nested repositories: %w", err)
	}
	return projectDaemonReposToCore(rows), nil
}

// ListProjectsWithUnsettledDaemonRepos returns the projects whose checkout
// on the machine has nested repositories still to clone (pending or
// cloning): what a connect resumes.
func ListProjectsWithUnsettledDaemonRepos(ctx context.Context, db orm.Context, daemonID string) ([]string, error) {
	var ids []string
	err := db.Bun().NewRaw(`SELECT DISTINCT project_id FROM project_daemon_repos
WHERE daemon_id = ? AND state IN ('pending', 'cloning') ORDER BY project_id`, daemonID).Scan(ctx, &ids)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("list projects with nested repositories to clone: %w", err)
	}
	return ids, nil
}
