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
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertPlan stores a new plan. created_at is p's when set; updated_at is the
// time of the write. Both are written back onto p.
func InsertPlan(ctx context.Context, db orm.Context, p *core.Plan) error {
	status := reliantv1.PlanStatus(p.Status)
	row := &Plan{
		ID:          p.ID,
		ThreadID:    p.ThreadID,
		Title:       p.Title,
		Description: p.Description,
		Status:      &status,
		Complexity:  planComplexityFromCore(p.Complexity),
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
		return nil, fmt.Errorf("plan not found: %s", id)
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

// ListPlansByProject returns the plans shared under the project, newest first.
func ListPlansByProject(ctx context.Context, db orm.Context, projectID string) ([]*core.Plan, error) {
	rows, err := ListPlan(ctx, db,
		orm.WhereEq(PlanFieldProjectID, projectID),
		orm.WithOrderBy(PlanFieldCreatedAt, orm.Desc))
	if err != nil {
		return nil, fmt.Errorf("failed to list plans by project: %w", err)
	}
	return plansToCore(rows), nil
}

// ReplacePlan rewrites the plan's mutable columns (title, description,
// status, complexity, completed_at) from p and stamps updated_at, which is
// written back onto p. A missing plan is orm.ErrNoRows.
func ReplacePlan(ctx context.Context, db orm.Context, p *core.Plan) error {
	status := reliantv1.PlanStatus(p.Status)
	row := &Plan{
		ID:          p.ID,
		Title:       p.Title,
		Description: p.Description,
		Status:      &status,
		Complexity:  planComplexityFromCore(p.Complexity),
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

// UpdatePlanStatus moves the plan to status, setting completed_at as given
// (nil clears it). A missing plan is orm.ErrNoRows.
func UpdatePlanStatus(ctx context.Context, db orm.Context, id string, status int32, completedAt *time.Time) error {
	s := reliantv1.PlanStatus(status)
	return UpdatePlanMasked(ctx, db, &Plan{ID: id, Status: &s, CompletedAt: completedAt},
		[]string{PlanFieldStatus, PlanFieldCompletedAt})
}

// RemovePlan deletes the plan row alone (tasks.plan_id has no foreign key).
// A missing plan is not an error.
func RemovePlan(ctx context.Context, db orm.Context, id string) error {
	if err := DeletePlan(ctx, db, id); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

func planComplexityFromCore(c *int32) *reliantv1.PlanComplexity {
	if c == nil {
		return nil
	}
	v := reliantv1.PlanComplexity(*c)
	return &v
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
		p.Status = int32(*row.Status)
	}
	if row.Complexity != nil {
		c := int32(*row.Complexity)
		p.Complexity = &c
	}
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
