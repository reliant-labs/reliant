// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for DefaultPresetAssignment. The generated default_preset_assignment_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: defaultPresetAssignmentRepo (a *crud.Repo[DefaultPresetAssignment]) and the
//     package-level GetDefaultPresetAssignmentByID / ListDefaultPresetAssignment / CreateDefaultPresetAssignment / UpdateDefaultPresetAssignment / DeleteDefaultPresetAssignment
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     DefaultPresetAssignment struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindDefaultPresetAssignmentBy(ctx context.Context, db orm.Context, /* args */) ([]*DefaultPresetAssignment, error) {
//		var rows []*DefaultPresetAssignment
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("workflow_name = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveDefaultPresetAssignments(ctx context.Context, db orm.Context) ([]*DefaultPresetAssignment, error) {
//		return ListDefaultPresetAssignment(ctx, db, orm.WhereEq("workflow_name", true))
//	}
package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
)

// GetDefaultPresetAssignments returns the workflow's default preset per
// group: group name to preset slug.
func GetDefaultPresetAssignments(ctx context.Context, db orm.Context, workflowName string) (map[string]string, error) {
	rows, err := ListDefaultPresetAssignment(ctx, db,
		orm.WhereEq(DefaultPresetAssignmentFieldWorkflowName, workflowName),
		orm.WithOrderBy(DefaultPresetAssignmentFieldGroupName, orm.Asc))
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.GroupName] = row.PresetSlug
	}
	return out, nil
}

// DefaultPresetAssignmentExtra carries fields on DefaultPresetAssignment that are NOT columns — computed
// values, transient state, anything derived rather than stored. DefaultPresetAssignment is
// embedded on DefaultPresetAssignmentExtra (see default_preset_assignment_orm.go), so a field added here is a field on
// every DefaultPresetAssignment value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type DefaultPresetAssignmentExtra struct {
}
