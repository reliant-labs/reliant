// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for OrgUpdate. The generated org_update_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: orgUpdateRepo (a *crud.Repo[OrgUpdate]) and the
//     package-level GetOrgUpdateByID / ListOrgUpdate / CreateOrgUpdate / UpdateOrgUpdate / DeleteOrgUpdate
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     OrgUpdate struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindOrgUpdateBy(ctx context.Context, db orm.Context, /* args */) ([]*OrgUpdate, error) {
//		var rows []*OrgUpdate
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("org_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveOrgUpdates(ctx context.Context, db orm.Context) ([]*OrgUpdate, error) {
//		return ListOrgUpdate(ctx, db, orm.WhereEq("org_id", true))
//	}
package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The org feed (org_updates) is written by the database itself (the
// org_feed_* triggers, in the transaction of the change each reports); these
// are its two reads, for StreamOrgUpdates.

// LatestOrgUpdateSequence is the highest sequence number an org's feed holds,
// 0 for an org with none.
func LatestOrgUpdateSequence(ctx context.Context, db orm.Context, orgID string) (int64, error) {
	var seq int64
	err := db.Bun().NewSelect().Model((*OrgUpdate)(nil)).
		ColumnExpr("COALESCE(MAX(sequence_number), 0)").
		Where("org_id = ?", orgID).
		Scan(ctx, &seq)
	return seq, err
}

// OrgUpdatesSince is an org's feed after sinceSeq, ascending, at most limit
// rows.
func OrgUpdatesSince(ctx context.Context, db orm.Context, orgID string, sinceSeq int64, limit int) ([]core.OrgUpdate, error) {
	rows, err := ListOrgUpdate(ctx, db,
		orm.WhereEq(OrgUpdateFieldOrgID, orgID),
		orm.WhereGt(OrgUpdateFieldSequenceNumber, sinceSeq),
		orm.WithOrderBy(OrgUpdateFieldSequenceNumber, orm.Asc),
		orm.WithLimit(limit))
	if err != nil {
		return nil, err
	}
	out := make([]core.OrgUpdate, len(rows))
	for i, r := range rows {
		// data is NOT NULL DEFAULT '{}': the ORM projects a defaulted column
		// as a pointer, but a row read back always has it.
		data := "{}"
		if r.Data != nil {
			data = *r.Data
		}
		out[i] = core.OrgUpdate{
			OrgID: r.OrgID, SequenceNumber: r.SequenceNumber, UpdateType: int32(r.UpdateType),
			Kind: core.ShareKind(r.ResourceKind), ResourceID: r.ResourceID, OwnerUserID: r.OwnerUserID,
			ProjectID: r.ProjectID, Title: r.Title, Data: data, CreatedAt: r.CreatedAt,
		}
	}
	return out, nil
}

// OrgUpdateExtra carries fields on OrgUpdate that are NOT columns — computed
// values, transient state, anything derived rather than stored. OrgUpdate is
// embedded on OrgUpdateExtra (see org_update_orm.go), so a field added here is a field on
// every OrgUpdate value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type OrgUpdateExtra struct {
}
