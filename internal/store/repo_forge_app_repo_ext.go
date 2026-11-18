// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for RepoForgeApp. The generated repo_forge_app_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: repoForgeAppRepo (a *crud.Repo[RepoForgeApp]) and the
//     package-level GetRepoForgeAppByPK / ListRepoForgeApp / CreateRepoForgeApp / UpsertRepoForgeApp, keyed by
//     the whole composite key (repo_id, subpath), plus UpdateRepoForgeApp / DeleteRepoForgeAppByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     RepoForgeApp struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindRepoForgeAppBy(ctx context.Context, db orm.Context, /* args */) ([]*RepoForgeApp, error) {
//		var rows []*RepoForgeApp
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("forge_name = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveRepoForgeApps(ctx context.Context, db orm.Context) ([]*RepoForgeApp, error) {
//		return ListRepoForgeApp(ctx, db, orm.WhereEq("forge_name", true))
//	}
package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// SetRepoForgeApps records the forge apps a daemon found in a repo. Run it in
// a transaction: it locks the repo's row first, so two reports for one repo
// apply one after the other, and a repo deleted meanwhile is a no-op.
//
// complete (the daemon's walk covered the whole repo) makes apps the repo's
// whole set: rows at other subpaths are deleted and repos.forge_apps_scanned_at
// is stamped. A partial walk only adds apps and renames ones it saw, since
// what it did not reach may still be there. An app whose name did not change
// is not rewritten, so a repeated report writes only the stamp.
func SetRepoForgeApps(ctx context.Context, db orm.Context, repoID string, apps []core.RepoForgeApp, complete bool) error {
	var locked []string
	if err := db.Bun().NewRaw(`SELECT id FROM repos WHERE id = ?0 FOR UPDATE`, repoID).Scan(ctx, &locked); err != nil {
		return fmt.Errorf("lock repo: %w", err)
	}
	if len(locked) == 0 {
		return nil
	}

	subpaths := make([]string, len(apps))
	names := make([]string, len(apps))
	for i, app := range apps {
		subpaths[i] = app.Subpath
		names[i] = app.Name
	}
	if complete {
		if _, err := db.Bun().NewRaw(`DELETE FROM repo_forge_apps WHERE repo_id = ?0 AND NOT (subpath = ANY(?1))`,
			repoID, orm.Arg(subpaths)).Exec(ctx); err != nil {
			return fmt.Errorf("delete repo forge apps: %w", err)
		}
	}
	if len(apps) > 0 {
		if _, err := db.Bun().NewRaw(`INSERT INTO repo_forge_apps (repo_id, subpath, forge_name)
SELECT ?0, t.subpath, t.forge_name FROM unnest(?1::text[], ?2::text[]) AS t(subpath, forge_name)
ON CONFLICT (repo_id, subpath) DO UPDATE SET forge_name = EXCLUDED.forge_name, updated_at = now()
WHERE repo_forge_apps.forge_name IS DISTINCT FROM EXCLUDED.forge_name`,
			repoID, orm.Arg(subpaths), orm.Arg(names)).Exec(ctx); err != nil {
			return fmt.Errorf("upsert repo forge apps: %w", err)
		}
	}
	if complete {
		if _, err := db.Bun().NewRaw(`UPDATE repos SET forge_apps_scanned_at = now() WHERE id = ?0`, repoID).Exec(ctx); err != nil {
			return fmt.Errorf("stamp repo forge-app scan: %w", err)
		}
	}
	return nil
}

// repoForgeAppRow is one row ListRepoForgeAppsByProject reads.
type repoForgeAppRow struct {
	RepoID    string `bun:"repo_id"`
	Subpath   string `bun:"subpath"`
	ForgeName string `bun:"forge_name"`
}

// ListRepoForgeAppsByProject returns every forge app recorded in the
// project's repos, ordered by repo and subpath.
func ListRepoForgeAppsByProject(ctx context.Context, db orm.Context, projectID string) ([]core.RepoForgeApp, error) {
	var rows []repoForgeAppRow
	if err := db.Bun().NewRaw(`SELECT a.repo_id, a.subpath, a.forge_name
FROM repo_forge_apps a JOIN repos r ON r.id = a.repo_id
WHERE r.project_id = ?0
ORDER BY a.repo_id, a.subpath`, projectID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list repo forge apps: %w", err)
	}
	out := make([]core.RepoForgeApp, len(rows))
	for i, row := range rows {
		out[i] = core.RepoForgeApp{RepoID: row.RepoID, Subpath: row.Subpath, Name: row.ForgeName}
	}
	return out, nil
}

// RepoForgeAppExtra carries fields on RepoForgeApp that are NOT columns — computed
// values, transient state, anything derived rather than stored. RepoForgeApp is
// embedded on RepoForgeAppExtra (see repo_forge_app_orm.go), so a field added here is a field on
// every RepoForgeApp value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type RepoForgeAppExtra struct {
}
