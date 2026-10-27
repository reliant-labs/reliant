// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for VisibilityOverride. The generated visibility_override_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: visibilityOverrideRepo (a *crud.Repo[VisibilityOverride]) and the
//     package-level GetVisibilityOverrideByID / ListVisibilityOverride / CreateVisibilityOverride / UpdateVisibilityOverride / DeleteVisibilityOverride
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     VisibilityOverride struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindVisibilityOverrideBy(ctx context.Context, db orm.Context, /* args */) ([]*VisibilityOverride, error) {
//		var rows []*VisibilityOverride
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveVisibilityOverrides(ctx context.Context, db orm.Context) ([]*VisibilityOverride, error) {
//		return ListVisibilityOverride(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// VisibilityOverrideExtra carries fields on VisibilityOverride that are NOT columns — computed
// values, transient state, anything derived rather than stored. VisibilityOverride is
// embedded on VisibilityOverrideExtra (see visibility_override_orm.go), so a field added here is a field on
// every VisibilityOverride value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type VisibilityOverrideExtra struct {
}

// GetVisibilityOverride returns the user's visibility override for an item,
// or nil when there is none.
func GetVisibilityOverride(ctx context.Context, db orm.Context, userID string, itemType reliantv1.HiddenItemType, slug string) (*bool, error) {
	rows, err := ListVisibilityOverride(ctx, db,
		orm.WhereEq(VisibilityOverrideFieldUserID, userID),
		orm.WhereEq(VisibilityOverrideFieldItemType, itemType),
		orm.WhereEq(VisibilityOverrideFieldSlug, slug),
		orm.WithLimit(1))
	if err != nil {
		return nil, fmt.Errorf("failed to get visibility override: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0].IsVisible, nil
}

// ListVisibilityOverrides returns the user's overrides for an item type,
// keyed by slug.
func ListVisibilityOverrides(ctx context.Context, db orm.Context, userID string, itemType reliantv1.HiddenItemType) (map[string]bool, error) {
	rows, err := ListVisibilityOverride(ctx, db,
		orm.WhereEq(VisibilityOverrideFieldUserID, userID),
		orm.WhereEq(VisibilityOverrideFieldItemType, itemType),
		orm.WithOrderBy(VisibilityOverrideFieldSlug, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list visibility overrides: %w", err)
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[row.Slug] = row.IsVisible
	}
	return out, nil
}

// SetVisibilityOverride records the user's visibility preference for an
// item, replacing any earlier one.
func SetVisibilityOverride(ctx context.Context, db orm.Context, userID string, itemType reliantv1.HiddenItemType, slug string, isVisible bool) error {
	row := &VisibilityOverride{
		ID: uuid.New().String(), UserID: userID, ItemType: itemType, Slug: slug,
		IsVisible: isVisible,
	}
	_, err := db.Bun().NewInsert().Model(row).
		Value(VisibilityOverrideFieldCreatedAt, "NOW()").
		On("CONFLICT (user_id, item_type, slug) DO UPDATE").
		Set("is_visible = EXCLUDED.is_visible").
		Exec(ctx)
	return err
}
