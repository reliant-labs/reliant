// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for TaskDependency. The generated task_dependency_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: taskDependencyRepo (a *crud.Repo[TaskDependency]) and the
//     package-level GetTaskDependencyByID / ListTaskDependency / CreateTaskDependency / UpdateTaskDependency / DeleteTaskDependency
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     TaskDependency struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindTaskDependencyBy(ctx context.Context, db orm.Context, /* args */) ([]*TaskDependency, error) {
//		var rows []*TaskDependency
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("from_task_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveTaskDependencies(ctx context.Context, db orm.Context) ([]*TaskDependency, error) {
//		return ListTaskDependency(ctx, db, orm.WhereEq("from_task_id", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/uptrace/bun"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertTaskDependency stores a new dependency edge. created_at is d's when
// set, else the column DEFAULT (now()); it is written back onto d. A second
// edge of the same (from, to, type) violates
// TaskDependencyConstraintFromTaskIDToTaskIDDependencyTypeKey.
func InsertTaskDependency(ctx context.Context, db orm.Context, d *core.TaskDependency) error {
	row := &TaskDependency{
		ID:             d.ID,
		FromTaskID:     d.FromTaskID,
		ToTaskID:       d.ToTaskID,
		DependencyType: reliantv1.DependencyType(d.DependencyType),
		CreatedAt:      d.CreatedAt,
	}
	if err := CreateTaskDependency(ctx, db, row); err != nil {
		return err
	}
	d.CreatedAt = row.CreatedAt
	return nil
}

// GetTaskDependency returns the dependency, or an error naming it when there
// is none.
func GetTaskDependency(ctx context.Context, db orm.Context, id string) (*core.TaskDependency, error) {
	row, err := GetTaskDependencyByID(ctx, db, id)
	if errors.Is(err, svcerr.ErrNotFound) {
		return nil, fmt.Errorf("task dependency not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get task dependency: %w", err)
	}
	return taskDependencyToCore(row), nil
}

// ListTaskDependenciesByTask returns every edge touching the task, either
// end, oldest first.
func ListTaskDependenciesByTask(ctx context.Context, db orm.Context, taskID string) ([]*core.TaskDependency, error) {
	rows, err := ListTaskDependency(ctx, db,
		func(q *bun.SelectQuery) {
			q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("from_task_id = ?", taskID).WhereOr("to_task_id = ?", taskID)
			})
		},
		orm.WithOrderBy(TaskDependencyFieldCreatedAt, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list task dependencies: %w", err)
	}
	return taskDependenciesToCore(rows), nil
}

// ListBlockersForTask returns the BLOCKS edges into the task, oldest first.
func ListBlockersForTask(ctx context.Context, db orm.Context, taskID string) ([]*core.TaskDependency, error) {
	rows, err := ListTaskDependency(ctx, db,
		orm.WhereEq(TaskDependencyFieldToTaskID, taskID),
		orm.WhereEq(TaskDependencyFieldDependencyType, reliantv1.DependencyType_DEPENDENCY_TYPE_BLOCKS),
		orm.WithOrderBy(TaskDependencyFieldCreatedAt, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list blockers: %w", err)
	}
	return taskDependenciesToCore(rows), nil
}

// ListDependenciesByPlan returns the edges leaving the plan's tasks, oldest
// first.
func ListDependenciesByPlan(ctx context.Context, db orm.Context, planID string) ([]*core.TaskDependency, error) {
	var rows []*TaskDependency
	err := db.Bun().NewSelect().Model(&rows).
		Join("JOIN tasks ON tasks.id = task_dependencies.from_task_id").
		Where("tasks.plan_id = ?", planID).
		OrderExpr("task_dependencies.created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list plan dependencies: %w", err)
	}
	return taskDependenciesToCore(rows), nil
}

// RemoveTaskDependency deletes the edge. A missing one is not an error.
func RemoveTaskDependency(ctx context.Context, db orm.Context, id string) error {
	if err := DeleteTaskDependency(ctx, db, id); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

// DeleteTaskDependencyByPair deletes the (from, to, type) edge, if any.
func DeleteTaskDependencyByPair(ctx context.Context, db orm.Context, fromTaskID, toTaskID string, depType int32) error {
	_, err := db.Bun().NewDelete().Model((*TaskDependency)(nil)).
		Where("from_task_id = ?", fromTaskID).
		Where("to_task_id = ?", toTaskID).
		Where("dependency_type = ?", reliantv1.DependencyType(depType)).
		Exec(ctx)
	return err
}

func taskDependencyToCore(row *TaskDependency) *core.TaskDependency {
	return &core.TaskDependency{
		ID:             row.ID,
		FromTaskID:     row.FromTaskID,
		ToTaskID:       row.ToTaskID,
		DependencyType: int32(row.DependencyType),
		CreatedAt:      row.CreatedAt,
	}
}

func taskDependenciesToCore(rows []*TaskDependency) []*core.TaskDependency {
	out := make([]*core.TaskDependency, len(rows))
	for i, row := range rows {
		out[i] = taskDependencyToCore(row)
	}
	return out
}

// TaskDependencyExtra carries fields on TaskDependency that are NOT columns — computed
// values, transient state, anything derived rather than stored. TaskDependency is
// embedded on TaskDependencyExtra (see task_dependency_orm.go), so a field added here is a field on
// every TaskDependency value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type TaskDependencyExtra struct {
}
