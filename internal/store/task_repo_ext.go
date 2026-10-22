// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Task. The generated task_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: taskRepo (a *crud.Repo[Task]) and the
//     package-level GetTaskByID / ListTask / CreateTask / UpdateTask / DeleteTask
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Task struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindTaskBy(ctx context.Context, db orm.Context, /* args */) ([]*Task, error) {
//		var rows []*Task
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("plan_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveTasks(ctx context.Context, db orm.Context) ([]*Task, error) {
//		return ListTask(ctx, db, orm.WhereEq("plan_id", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertTask stores a new task. created_at is t's when set; updated_at is the
// time of the write. Both are written back onto t.
func InsertTask(ctx context.Context, db orm.Context, t *core.Task) error {
	status := reliantv1.TaskStatus(t.Status)
	row := &Task{
		ID:           t.ID,
		PlanID:       t.PlanID,
		ParentTaskID: t.ParentTaskID,
		Title:        t.Title,
		Description:  t.Description,
		Status:       &status,
		Position:     int64(t.Position),
		Metadata:     t.Metadata,
		Assignee:     t.Assignee,
		CreatedAt:    t.CreatedAt,
		CompletedAt:  t.CompletedAt,
	}
	if err := CreateTask(ctx, db, row); err != nil {
		return err
	}
	t.CreatedAt, t.UpdatedAt = row.CreatedAt, row.UpdatedAt
	return nil
}

// GetTask returns the task, or an error naming it when there is none.
func GetTask(ctx context.Context, db orm.Context, id string) (*core.Task, error) {
	row, err := GetTaskByID(ctx, db, id)
	if errors.Is(err, svcerr.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("task"), id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	return taskToCore(row), nil
}

// ListTasksByPlan returns the plan's tasks by position.
func ListTasksByPlan(ctx context.Context, db orm.Context, planID string) ([]*core.Task, error) {
	rows, err := ListTask(ctx, db,
		orm.WhereEq(TaskFieldPlanID, planID),
		orm.WithOrderBy(TaskFieldPosition, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}
	out := make([]*core.Task, len(rows))
	for i, row := range rows {
		out[i] = taskToCore(row)
	}
	return out, nil
}

// ReplaceTask rewrites the task's mutable columns (title, description,
// status, position, metadata, assignee, completed_at) from t and stamps
// updated_at, which is written back onto t. A missing task is orm.ErrNoRows.
func ReplaceTask(ctx context.Context, db orm.Context, t *core.Task) error {
	status := reliantv1.TaskStatus(t.Status)
	row := &Task{
		ID:          t.ID,
		Title:       t.Title,
		Description: t.Description,
		Status:      &status,
		Position:    int64(t.Position),
		Metadata:    t.Metadata,
		Assignee:    t.Assignee,
		CompletedAt: t.CompletedAt,
	}
	err := UpdateTaskMasked(ctx, db, row, []string{
		TaskFieldTitle, TaskFieldDescription, TaskFieldStatus, TaskFieldPosition,
		TaskFieldMetadata, TaskFieldAssignee, TaskFieldCompletedAt,
	})
	if err != nil {
		return err
	}
	t.UpdatedAt = row.UpdatedAt
	return nil
}

// UpdateTaskStatus moves the task to status, setting completed_at as given
// (nil clears it). A missing task is orm.ErrNoRows.
func UpdateTaskStatus(ctx context.Context, db orm.Context, id string, status int32, completedAt *time.Time) error {
	s := reliantv1.TaskStatus(status)
	return UpdateTaskMasked(ctx, db, &Task{ID: id, Status: &s, CompletedAt: completedAt},
		[]string{TaskFieldStatus, TaskFieldCompletedAt})
}

// RemoveTask deletes the task; its dependencies go with it (ON DELETE
// CASCADE on both ends). A missing task is not an error.
func RemoveTask(ctx context.Context, db orm.Context, id string) error {
	if err := DeleteTask(ctx, db, id); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

func taskToCore(row *Task) *core.Task {
	t := &core.Task{
		ID:           row.ID,
		PlanID:       row.PlanID,
		ParentTaskID: row.ParentTaskID,
		Title:        row.Title,
		Description:  row.Description,
		Position:     int(row.Position),
		Metadata:     row.Metadata,
		Assignee:     row.Assignee,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		CompletedAt:  row.CompletedAt,
	}
	if row.Status != nil {
		t.Status = int32(*row.Status)
	}
	return t
}

// TaskExtra carries fields on Task that are NOT columns — computed
// values, transient state, anything derived rather than stored. Task is
// embedded on TaskExtra (see task_orm.go), so a field added here is a field on
// every Task value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type TaskExtra struct {
}
