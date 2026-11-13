// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for OrgProviderWorkspace. The generated org_provider_workspace_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: orgProviderWorkspaceRepo (a *crud.Repo[OrgProviderWorkspace]) and the
//     package-level GetOrgProviderWorkspaceByPK / ListOrgProviderWorkspace / CreateOrgProviderWorkspace / UpsertOrgProviderWorkspace, keyed by
//     the whole composite key (org_id, provider, external_org_id), plus UpdateOrgProviderWorkspace / DeleteOrgProviderWorkspaceByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     OrgProviderWorkspace struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindOrgProviderWorkspaceBy(ctx context.Context, db orm.Context, /* args */) ([]*OrgProviderWorkspace, error) {
//		var rows []*OrgProviderWorkspace
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("display_name = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveOrgProviderWorkspaces(ctx context.Context, db orm.Context) ([]*OrgProviderWorkspace, error) {
//		return ListOrgProviderWorkspace(ctx, db, orm.WhereEq("display_name", true))
//	}
package store

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Provider workspaces an org pays for (org-provider-accounts.md §3.4).

func orgProviderWorkspaceToCore(r *OrgProviderWorkspace) core.OrgProviderWorkspace {
	return core.OrgProviderWorkspace{
		OrgID: r.OrgID, Provider: r.Provider, ExternalOrgID: r.ExternalOrgID, DisplayName: r.DisplayName,
		BoundByUserID: r.BoundByUserID, BoundAt: r.BoundAt,
	}
}

// ListOrgProviderWorkspacesOf returns orgID's bindings, by (provider, bound_at).
func ListOrgProviderWorkspacesOf(ctx context.Context, db orm.Context, orgID string) ([]core.OrgProviderWorkspace, error) {
	if orgID == "" {
		return nil, nil
	}
	rows, err := ListOrgProviderWorkspace(ctx, db, orm.WhereEq(OrgProviderWorkspaceFieldOrgID, orgID),
		orm.WithOrderBy(OrgProviderWorkspaceFieldProvider, orm.Asc), orm.WithOrderBy(OrgProviderWorkspaceFieldBoundAt, orm.Asc))
	if err != nil {
		return nil, err
	}
	out := make([]core.OrgProviderWorkspace, 0, len(rows))
	for _, r := range rows {
		out = append(out, orgProviderWorkspaceToCore(r))
	}
	return out, nil
}

// InsertOrgProviderWorkspace binds w to its org. created is false, and the stored binding is
// returned, when the org already holds it. A workspace bound to ANOTHER org fails with the
// unique violation of OrgProviderWorkspaceConstraintOneOrg, which the caller classifies.
func InsertOrgProviderWorkspace(ctx context.Context, db orm.Context, w core.OrgProviderWorkspace, now time.Time) (core.OrgProviderWorkspace, bool, error) {
	row := &OrgProviderWorkspace{
		OrgID: w.OrgID, Provider: w.Provider, ExternalOrgID: w.ExternalOrgID, DisplayName: w.DisplayName,
		BoundByUserID: w.BoundByUserID, BoundAt: now.UTC(),
	}
	res, err := db.Bun().NewInsert().Model(row).On("CONFLICT (org_id, provider, external_org_id) DO NOTHING").Exec(ctx)
	if err != nil {
		return core.OrgProviderWorkspace{}, false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return core.OrgProviderWorkspace{}, false, err
	} else if n > 0 {
		return orgProviderWorkspaceToCore(row), true, nil
	}
	cur, err := GetOrgProviderWorkspaceByPK(ctx, db, w.OrgID, w.Provider, w.ExternalOrgID)
	if err != nil {
		return core.OrgProviderWorkspace{}, false, err
	}
	return orgProviderWorkspaceToCore(cur), false, nil
}

// DeleteOrgProviderWorkspace removes one of orgID's bindings and reports whether there was one.
func DeleteOrgProviderWorkspace(ctx context.Context, db orm.Context, orgID, provider, externalOrgID string) (bool, error) {
	res, err := db.Bun().NewDelete().Model((*OrgProviderWorkspace)(nil)).
		Where("org_id = ?", orgID).Where("provider = ?", provider).Where("external_org_id = ?", externalOrgID).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// OrgProviderWorkspaceExtra carries fields on OrgProviderWorkspace that are NOT columns — computed
// values, transient state, anything derived rather than stored. OrgProviderWorkspace is
// embedded on OrgProviderWorkspaceExtra (see org_provider_workspace_orm.go), so a field added here is a field on
// every OrgProviderWorkspace value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type OrgProviderWorkspaceExtra struct {
}
