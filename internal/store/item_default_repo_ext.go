// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ItemDefault. The generated item_default_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: itemDefaultRepo (a *crud.Repo[ItemDefault]) and the
//     package-level GetItemDefaultByID / ListItemDefault / CreateItemDefault / UpdateItemDefault / DeleteItemDefault
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ItemDefault struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindItemDefaultBy(ctx context.Context, db orm.Context, /* args */) ([]*ItemDefault, error) {
//		var rows []*ItemDefault
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("item_type = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveItemDefaults(ctx context.Context, db orm.Context) ([]*ItemDefault, error) {
//		return ListItemDefault(ctx, db, orm.WhereEq("item_type", true))
//	}
package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// GetItemDefault returns the system default visibility of a builtin workflow
// or preset, or sql.ErrNoRows when it has none.
func GetItemDefault(ctx context.Context, db orm.Context, itemType int32, slug string) (*core.ItemDefault, error) {
	rows, err := ListItemDefault(ctx, db,
		orm.WhereEq(ItemDefaultFieldItemType, reliantv1.HiddenItemType(itemType)),
		orm.WhereEq(ItemDefaultFieldSlug, slug),
		orm.WithLimit(1))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, missing("item default")
	}
	row := rows[0]
	return &core.ItemDefault{ItemType: itemType, Slug: slug, IsHidden: row.IsHidden, Reason: row.Reason}, nil
}

// ListHiddenItemDefaults returns the slugs of the items of itemType hidden by
// default, in order.
func ListHiddenItemDefaults(ctx context.Context, db orm.Context, itemType int32) ([]string, error) {
	rows, err := ListItemDefault(ctx, db,
		orm.WhereEq(ItemDefaultFieldItemType, reliantv1.HiddenItemType(itemType)),
		orm.WhereEq(ItemDefaultFieldIsHidden, true),
		orm.WithOrderBy(ItemDefaultFieldSlug, orm.Asc))
	if err != nil {
		return nil, err
	}
	slugs := make([]string, len(rows))
	for i, row := range rows {
		slugs[i] = row.Slug
	}
	return slugs, nil
}

// ItemDefaultExtra carries fields on ItemDefault that are NOT columns — computed
// values, transient state, anything derived rather than stored. ItemDefault is
// embedded on ItemDefaultExtra (see item_default_orm.go), so a field added here is a field on
// every ItemDefault value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ItemDefaultExtra struct {
}
