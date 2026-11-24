// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for InsightsDrilldown. The generated insights_drilldown_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: insightsDrilldownRepo (a *crud.Repo[InsightsDrilldown]) and the
//     package-level GetInsightsDrilldownByID / ListInsightsDrilldown / CreateInsightsDrilldown / UpdateInsightsDrilldown / DeleteInsightsDrilldown
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     InsightsDrilldown struct (or your own row type).
//     Placeholders follow the path: bun's `?` off db.Bun() (builders,
//     NewRaw); postgres's $1, $2, … in db.Exec / db.Query / db.QueryRow,
//     which send the SQL as written (`?` there is the jsonb operator).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Each exported func(ctx context.Context, db orm.Context, …) in this file is
// also a method of InsightsDrilldownExtStore (insights_drilldown_ext_store_gen.go, regenerated
// from this file every forge generate), the handle bound: name that in a
// service's Deps — forge wires and mocks it — instead of hand-writing an
// adapter over these functions.
//
// Example (uncomment and adapt):
//
//	func FindInsightsDrilldownBy(ctx context.Context, db orm.Context, /* args */) ([]*InsightsDrilldown, error) {
//		var rows []*InsightsDrilldown
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("org_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveInsightsDrilldowns(ctx context.Context, db orm.Context) ([]*InsightsDrilldown, error) {
//		return ListInsightsDrilldown(ctx, db, orm.WhereEq("org_id", true))
//	}
//
// A write over many rows is ONE set-based statement, never List then an
// UpdateInsightsDrilldownMasked per row (N round trips: it times out at scale). The
// generated UpdateInsightsDrilldownWhere(ctx, db, values, fields, filters...) is that
// statement over List's own filters. For one it cannot express, go through
// db.Bun() like any raw query (there is no orm.Ident — name columns in the
// SQL, values as `?` arguments):
//
//	func MoveInsightsDrilldowns(ctx context.Context, db orm.Context, from, to any) (int64, error) {
//		res, err := db.Bun().NewUpdate().Model((*InsightsDrilldown)(nil)).
//			Set("org_id = ?", to).
//			Where("org_id = ?", from).
//			Exec(ctx)
//		if err != nil {
//			return 0, err
//		}
//		return res.RowsAffected()
//	}
package store

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// The audit of Insights member drill-downs (dev-docs/design/agent-analytics.md
// §3.6, PR C1; migration 20261124195542_insights_drilldowns). Append-only:
// rows are created by CreateInsightsDrilldown and read here, never updated.

// InsightsDrilldownCursor is the position after the last row of a page:
// pages run newest first, by (created_at, id).
type InsightsDrilldownCursor struct {
	CreatedAt time.Time
	ID        string
}

// ListInsightsDrilldowns is up to limit of orgID's drill-down records, newest
// first, after the cursor when it is non-nil. subjectUserID, when not empty,
// keeps only the drill-downs into that member: a member reads the records
// about themselves, an org admin every record of the org.
func ListInsightsDrilldowns(ctx context.Context, db orm.Context, orgID, subjectUserID string, after *InsightsDrilldownCursor, limit int) ([]*InsightsDrilldown, error) {
	if orgID == "" {
		return nil, errors.New("insights drilldowns: org id is required")
	}
	if limit <= 0 {
		return nil, errors.New("insights drilldowns: limit must be positive")
	}
	var rows []*InsightsDrilldown
	q := db.Bun().NewSelect().Model(&rows).Where("org_id = ?", orgID)
	if subjectUserID != "" {
		q = q.Where("subject_user_id = ?", subjectUserID)
	}
	if after != nil {
		q = q.Where("(created_at, id) < (?, ?)", after.CreatedAt, after.ID)
	}
	err := q.OrderExpr("created_at DESC, id DESC").Limit(int64(limit)).Scan(ctx)
	return rows, err
}

// InsightsDrilldownExtra carries fields on InsightsDrilldown that are NOT columns — computed
// values, transient state, anything derived rather than stored. InsightsDrilldown is
// embedded on InsightsDrilldownExtra (see insights_drilldown_orm.go), so a field added here is a field on
// every InsightsDrilldown value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type InsightsDrilldownExtra struct {
}
