// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Plan. The generated plan_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: planRepo (a *crud.Repo[Plan]) and the
//     package-level GetPlanByID / ListPlan / CreatePlan / UpdatePlan / DeletePlan
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Plan struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindPlanBy(ctx context.Context, db orm.Context, /* args */) ([]*Plan, error) {
//		var rows []*Plan
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("title = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActivePlans(ctx context.Context, db orm.Context) ([]*Plan, error) {
//		return ListPlan(ctx, db, orm.WhereEq("title", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertPlan stores a new plan. created_at is p's when set; updated_at is the
// time of the write. Both are written back onto p.
func InsertPlan(ctx context.Context, db orm.Context, p *core.Plan) error {
	status := p.Status
	row := &Plan{
		ID:          p.ID,
		ThreadID:    p.ThreadID,
		Title:       p.Title,
		Description: p.Description,
		Status:      &status,
		Complexity:  p.Complexity,
		ProjectID:   p.ProjectID,
		CreatedAt:   p.CreatedAt,
		CompletedAt: p.CompletedAt,
	}
	if err := CreatePlan(ctx, db, row); err != nil {
		return err
	}
	p.CreatedAt, p.UpdatedAt = row.CreatedAt, row.UpdatedAt
	return nil
}

// GetPlan returns the plan, or an error naming it when there is none.
func GetPlan(ctx context.Context, db orm.Context, id string) (*core.Plan, error) {
	row, err := GetPlanByID(ctx, db, id)
	if errors.Is(err, svcerr.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("plan"), id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get plan: %w", err)
	}
	return planToCore(row), nil
}

// GetPlanByThreadID returns the thread's newest plan, or core.ErrPlanNotFound.
func GetPlanByThreadID(ctx context.Context, db orm.Context, threadID string) (*core.Plan, error) {
	rows, err := ListPlan(ctx, db,
		orm.WhereEq(PlanFieldThreadID, threadID),
		orm.WithOrderBy(PlanFieldCreatedAt, orm.Desc),
		orm.WithLimit(1))
	if err != nil {
		return nil, fmt.Errorf("failed to list plans: %w", err)
	}
	if len(rows) == 0 {
		return nil, core.ErrPlanNotFound
	}
	return planToCore(rows[0]), nil
}

// ListPlansByThread returns the thread's plans, newest first.
func ListPlansByThread(ctx context.Context, db orm.Context, threadID string) ([]*core.Plan, error) {
	rows, err := ListPlan(ctx, db,
		orm.WhereEq(PlanFieldThreadID, threadID),
		orm.WithOrderBy(PlanFieldCreatedAt, orm.Desc))
	if err != nil {
		return nil, fmt.Errorf("failed to list plans: %w", err)
	}
	return plansToCore(rows), nil
}

// ListPlansByChatID returns the plans of every thread of the chat, newest
// first.
func ListPlansByChatID(ctx context.Context, db orm.Context, chatID string) ([]*core.Plan, error) {
	var rows []*Plan
	err := db.Bun().NewSelect().Model(&rows).
		Join("JOIN threads ON threads.id = plans.thread_id").
		Where("threads.chat_id = ?", chatID).
		OrderExpr("plans.created_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list plans by chat: %w", err)
	}
	return plansToCore(rows), nil
}

// copyChatPlansSQL copies every plan of a chat (all its threads, as
// ListPlansByChatID reads them) and every task of those plans onto one
// thread, in one statement.
//
// Each copy gets a fresh id; a task's parent is mapped to its parent's copy
// (a parent outside the task's own plan maps to none). Plans and tasks start
// over as pending with no completion. A copied plan keeps its source's
// created_at, so the branch lists its plans in the source's order, newest
// first.
//
// One statement, not a round trip per row: a chat with 20 plans and 116 tasks
// took ~140 sequential inserts, about half a second, inside BranchChat's
// transaction. The tasks are copied from the plans ins_plans RETURNED, so the
// task insert depends on the plan insert and cannot copy a task whose plan
// was not inserted. Their foreign keys to those plans are checked after the
// whole statement, when the plan rows exist.
const copyChatPlansSQL = `
WITH src_plans AS (
    SELECT p.id, p.title, p.description, p.complexity, p.created_at,
           gen_random_uuid()::text AS new_id
    FROM plans p
    JOIN threads t ON t.id = p.thread_id
    WHERE t.chat_id = ?0
),
ins_plans AS (
    INSERT INTO plans (id, thread_id, title, description, status, complexity, created_at, updated_at)
    SELECT new_id, ?1, title, description, ?2, complexity, created_at, now()
    FROM src_plans
    RETURNING id
),
src_tasks AS (
    SELECT tk.id, tk.parent_task_id, tk.title, tk.description, tk.position,
           sp.new_id AS new_plan_id,
           gen_random_uuid()::text AS new_id
    FROM tasks tk
    JOIN src_plans sp ON sp.id = tk.plan_id
    JOIN ins_plans ip ON ip.id = sp.new_id
)
INSERT INTO tasks (id, plan_id, parent_task_id, title, description, status, position, created_at, updated_at)
SELECT s.new_id, s.new_plan_id, parent.new_id, s.title, s.description, ?3, s.position, now(), now()
FROM src_tasks s
LEFT JOIN src_tasks parent ON parent.id = s.parent_task_id AND parent.new_plan_id = s.new_plan_id`

// CopyChatPlans gives toThreadID a copy of every plan of fromChatID, with
// their tasks, reset to planStatus and taskStatus (see copyChatPlansSQL).
func CopyChatPlans(ctx context.Context, db orm.Context, fromChatID, toThreadID string, planStatus, taskStatus int32) error {
	if fromChatID == "" || toThreadID == "" {
		return fmt.Errorf("both the source chat and the target thread are required")
	}
	if _, err := db.Bun().NewRaw(copyChatPlansSQL, fromChatID, toThreadID, planStatus, taskStatus).Exec(ctx); err != nil {
		return fmt.Errorf("failed to copy the chat's plans: %w", err)
	}
	return nil
}

// ReplacePlan rewrites the plan's mutable columns (title, description,
// status, complexity, completed_at) from p and stamps updated_at, which is
// written back onto p. A missing plan is orm.ErrNoRows.
func ReplacePlan(ctx context.Context, db orm.Context, p *core.Plan) error {
	status := p.Status
	row := &Plan{
		ID:          p.ID,
		Title:       p.Title,
		Description: p.Description,
		Status:      &status,
		Complexity:  p.Complexity,
		CompletedAt: p.CompletedAt,
	}
	err := UpdatePlanMasked(ctx, db, row, []string{
		PlanFieldTitle, PlanFieldDescription, PlanFieldStatus, PlanFieldComplexity, PlanFieldCompletedAt,
	})
	if err != nil {
		return err
	}
	p.UpdatedAt = row.UpdatedAt
	return nil
}

// RemovePlan deletes the plan row alone (tasks.plan_id has no foreign key).
// A missing plan is not an error.
func RemovePlan(ctx context.Context, db orm.Context, id string) error {
	if err := DeletePlan(ctx, db, id); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

func planToCore(row *Plan) *core.Plan {
	p := &core.Plan{
		ID:          row.ID,
		ThreadID:    row.ThreadID,
		Title:       row.Title,
		Description: row.Description,
		ProjectID:   row.ProjectID,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		CompletedAt: row.CompletedAt,
	}
	if row.Status != nil {
		p.Status = *row.Status
	}
	p.Complexity = row.Complexity
	return p
}

func plansToCore(rows []*Plan) []*core.Plan {
	out := make([]*core.Plan, len(rows))
	for i, row := range rows {
		out[i] = planToCore(row)
	}
	return out
}

// PlanExtra carries fields on Plan that are NOT columns — computed
// values, transient state, anything derived rather than stored. Plan is
// embedded on PlanExtra (see plan_orm.go), so a field added here is a field on
// every Plan value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type PlanExtra struct {
}
