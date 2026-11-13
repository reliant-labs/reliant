// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ProviderAccountGrant. The generated provider_account_grant_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: providerAccountGrantRepo (a *crud.Repo[ProviderAccountGrant]) and the
//     package-level GetProviderAccountGrantByPK / ListProviderAccountGrant / CreateProviderAccountGrant / UpsertProviderAccountGrant, keyed by
//     the whole composite key (account_id, user_id), plus UpdateProviderAccountGrant / DeleteProviderAccountGrantByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ProviderAccountGrant struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindProviderAccountGrantBy(ctx context.Context, db orm.Context, /* args */) ([]*ProviderAccountGrant, error) {
//		var rows []*ProviderAccountGrant
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("org_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveProviderAccountGrants(ctx context.Context, db orm.Context) ([]*ProviderAccountGrant, error) {
//		return ListProviderAccountGrant(ctx, db, orm.WhereEq("org_id", true))
//	}
package store

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Grants of an org account whose audience is 'selected' (org-provider-accounts.md §3.3). The
// caller (internal/db's accountStore) has already locked the account and checked it is orgID's:
// nothing here re-derives an account's org.

// ListOrgProviderAccountGrants returns every grant of orgID's accounts, by (account, user).
func ListOrgProviderAccountGrants(ctx context.Context, db orm.Context, orgID string) ([]core.OrgAccountGrant, error) {
	if orgID == "" {
		return nil, nil
	}
	rows, err := ListProviderAccountGrant(ctx, db, orm.WhereEq(ProviderAccountGrantFieldOrgID, orgID),
		orm.WithOrderBy(ProviderAccountGrantFieldAccountID, orm.Asc), orm.WithOrderBy(ProviderAccountGrantFieldUserID, orm.Asc))
	if err != nil {
		return nil, err
	}
	out := make([]core.OrgAccountGrant, 0, len(rows))
	for _, r := range rows {
		out = append(out, core.OrgAccountGrant{
			AccountID: r.AccountID, OrgID: r.OrgID, UserID: r.UserID,
			GrantedByUserID: r.GrantedByUserID, GrantedAt: r.GrantedAt,
		})
	}
	return out, nil
}

// ReplaceProviderAccountGrants makes userIDs exactly the members granted accountID (of orgID),
// each granted by grantedBy at now; a member not listed loses their grant. A listed member's
// existing row is RENEWED (granted_at = now), not kept: a grant counts only from the membership
// it was made in (granted_at >= member_since), and the row may be a stale one from before the
// member left and rejoined. The caller has just checked every listed user is a live member, so
// now is within their current membership; renewing a continuing member's grant changes nothing
// about whether it counts.
func ReplaceProviderAccountGrants(ctx context.Context, db orm.Context, orgID, accountID string, userIDs []string, grantedBy string, now time.Time) error {
	del := db.Bun().NewDelete().Model((*ProviderAccountGrant)(nil)).Where("account_id = ?", accountID)
	if len(userIDs) > 0 {
		del = del.Where("user_id NOT IN (?)", bun.List(userIDs))
	}
	if _, err := del.Exec(ctx); err != nil {
		return err
	}
	if len(userIDs) == 0 {
		return nil
	}
	rows := make([]ProviderAccountGrant, 0, len(userIDs))
	for _, u := range userIDs {
		rows = append(rows, ProviderAccountGrant{AccountID: accountID, OrgID: orgID, UserID: u, GrantedByUserID: grantedBy, GrantedAt: now.UTC()})
	}
	_, err := db.Bun().NewInsert().Model(&rows).On("CONFLICT (account_id, user_id) DO UPDATE").
		Set("org_id = EXCLUDED.org_id").Set("granted_by_user_id = EXCLUDED.granted_by_user_id").
		Set("granted_at = EXCLUDED.granted_at").Exec(ctx)
	return err
}

// ProviderAccountGrantExtra carries fields on ProviderAccountGrant that are NOT columns — computed
// values, transient state, anything derived rather than stored. ProviderAccountGrant is
// embedded on ProviderAccountGrantExtra (see provider_account_grant_orm.go), so a field added here is a field on
// every ProviderAccountGrant value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ProviderAccountGrantExtra struct {
}
