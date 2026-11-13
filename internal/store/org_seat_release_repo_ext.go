// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for OrgSeatRelease. The generated org_seat_release_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: orgSeatReleaseRepo (a *crud.Repo[OrgSeatRelease]) and the
//     package-level GetOrgSeatReleaseByPK / ListOrgSeatRelease / CreateOrgSeatRelease / UpsertOrgSeatRelease, keyed by
//     the whole composite key (org_id, account_id), plus UpdateOrgSeatRelease / DeleteOrgSeatReleaseByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     OrgSeatRelease struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindOrgSeatReleaseBy(ctx context.Context, db orm.Context, /* args */) ([]*OrgSeatRelease, error) {
//		var rows []*OrgSeatRelease
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("released_by_user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveOrgSeatReleases(ctx context.Context, db orm.Context) ([]*OrgSeatRelease, error) {
//		return ListOrgSeatRelease(ctx, db, orm.WhereEq("released_by_user_id", true))
//	}
package store

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// Seats an org admin released (org-provider-accounts.md §6): the binding matcher never makes
// such an account the org's seat again (AttributeWorkspaceSeat).

// InsertOrgSeatRelease records that releasedBy released accountID from orgID's seats; a second
// release of the same seat restamps it.
func InsertOrgSeatRelease(ctx context.Context, db orm.Context, orgID, accountID, releasedBy string, now time.Time) error {
	row := &OrgSeatRelease{OrgID: orgID, AccountID: accountID, ReleasedByUserID: releasedBy, ReleasedAt: now.UTC()}
	_, err := db.Bun().NewInsert().Model(row).
		On("CONFLICT (org_id, account_id) DO UPDATE").
		Set("released_by_user_id = EXCLUDED.released_by_user_id").Set("released_at = EXCLUDED.released_at").
		Exec(ctx)
	return err
}

// DeleteWorkspaceSeatReleases forgets orgID's releases of the sign-ins in provider's workspace
// externalOrgID (an unbind: binding the workspace again starts afresh), and returns how many.
func DeleteWorkspaceSeatReleases(ctx context.Context, db orm.Context, orgID, provider, externalOrgID string) (int64, error) {
	res, err := db.Bun().NewRaw(`DELETE FROM org_seat_releases r USING provider_accounts p
		WHERE r.account_id = p.id AND r.org_id = ?0 AND p.provider = ?1 AND `+providerWorkspaceIDOf("p")+` = ?2`,
		orgID, provider, externalOrgID).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// OrgSeatReleaseExtra carries fields on OrgSeatRelease that are NOT columns — computed
// values, transient state, anything derived rather than stored. OrgSeatRelease is
// embedded on OrgSeatReleaseExtra (see org_seat_release_orm.go), so a field added here is a field on
// every OrgSeatRelease value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type OrgSeatReleaseExtra struct {
}
