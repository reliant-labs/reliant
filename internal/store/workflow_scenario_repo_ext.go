// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// The workflow_scenarios queries (P5b lane L2): event sequences a workflow is
// simulated against, owned by a user and usually by one workflow draft
// (NULL when the scenario tests raw YAML). Every write bumps version, the
// optimistic-concurrency counter the scenario tools compare.
package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertWorkflowScenario writes a new scenario at version 1, never run,
// with the caller's created_at and updated_at.
func InsertWorkflowScenario(ctx context.Context, db orm.Context, s *core.WorkflowScenario) error {
	return CreateWorkflowScenario(ctx, db, &WorkflowScenario{
		ID:              s.ID,
		WorkflowDraftID: nullStringPtr(s.WorkflowDraftID),
		UserID:          s.UserID,
		Name:            s.Name,
		Description:     nullStringPtr(s.Description),
		Events:          s.Events,
		Expect:          nullStringPtr(s.Expect),
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
		Version:         1,
	})
}

// FindWorkflowScenario is scenario id, or nil.
func FindWorkflowScenario(ctx context.Context, db orm.Context, id string) (*core.WorkflowScenario, error) {
	return findWorkflowScenario(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery { return q.Where("id = ?", id) })
}

// FindWorkflowScenarioByName is the draft's scenario called name, or nil.
func FindWorkflowScenarioByName(ctx context.Context, db orm.Context, draftID, name string) (*core.WorkflowScenario, error) {
	return findWorkflowScenario(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("workflow_draft_id = ?", draftID).Where("name = ?", name)
	})
}

func findWorkflowScenario(ctx context.Context, db orm.Context, where func(*bun.SelectQuery) *bun.SelectQuery) (*core.WorkflowScenario, error) {
	row := new(WorkflowScenario)
	err := where(db.Bun().NewSelect().Model(row)).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return workflowScenarioToCore(row), nil
}

// ListDraftWorkflowScenarios is a draft's scenarios, newest first.
func ListDraftWorkflowScenarios(ctx context.Context, db orm.Context, draftID string) ([]*core.WorkflowScenario, error) {
	return selectWorkflowScenarios(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("workflow_draft_id = ?", draftID).Order("created_at DESC")
	})
}

// ListUserWorkflowScenarios is a user's scenarios, most recently updated
// first.
func ListUserWorkflowScenarios(ctx context.Context, db orm.Context, userID string) ([]*core.WorkflowScenario, error) {
	return selectWorkflowScenarios(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("user_id = ?", userID).Order("updated_at DESC")
	})
}

func selectWorkflowScenarios(ctx context.Context, db orm.Context, apply func(*bun.SelectQuery) *bun.SelectQuery) ([]*core.WorkflowScenario, error) {
	var rows []*WorkflowScenario
	if err := apply(db.Bun().NewSelect().Model(&rows)).Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]*core.WorkflowScenario, len(rows))
	for i, row := range rows {
		out[i] = workflowScenarioToCore(row)
	}
	return out, nil
}

// ReplaceWorkflowScenario writes s's editable fields — name, description,
// events, expect — over scenario s.ID. A missing scenario is sql.ErrNoRows.
func ReplaceWorkflowScenario(ctx context.Context, db orm.Context, s *core.WorkflowScenario) error {
	return updateWorkflowScenario(ctx, db, s.ID, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("name = ?", s.Name).
			Set("description = ?", nullStringPtr(s.Description)).
			Set("events = ?", s.Events).
			Set("expect = ?", nullStringPtr(s.Expect))
	})
}

// RecordWorkflowScenarioResult caches the outcome of a run of the scenario,
// stamped now. A missing scenario is sql.ErrNoRows.
func RecordWorkflowScenarioResult(ctx context.Context, db orm.Context, id, status, result string) error {
	return updateWorkflowScenario(ctx, db, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("last_run_at = NOW()").
			Set("last_run_status = ?", status).
			Set("last_run_result = ?", result)
	})
}

func updateWorkflowScenario(ctx context.Context, db orm.Context, id string, set func(*bun.UpdateQuery) *bun.UpdateQuery) error {
	res, err := set(db.Bun().NewUpdate().Model((*WorkflowScenario)(nil))).
		Set("updated_at = NOW()").
		Set("version = version + 1").
		Where("id = ?", id).
		Exec(ctx)
	return requireOneRow(res, err, "workflow scenario")
}

// RemoveWorkflowScenario deletes one scenario; a missing one is not an
// error.
func RemoveWorkflowScenario(ctx context.Context, db orm.Context, id string) error {
	return ignoreNotFound(DeleteWorkflowScenario(ctx, db, id))
}

// RemoveDraftWorkflowScenarios deletes every scenario of a draft.
func RemoveDraftWorkflowScenarios(ctx context.Context, db orm.Context, draftID string) error {
	_, err := db.Bun().NewDelete().Model((*WorkflowScenario)(nil)).Where("workflow_draft_id = ?", draftID).Exec(ctx)
	return err
}

func workflowScenarioToCore(row *WorkflowScenario) *core.WorkflowScenario {
	return &core.WorkflowScenario{
		ID:              row.ID,
		WorkflowDraftID: toNullString(row.WorkflowDraftID),
		UserID:          row.UserID,
		Name:            row.Name,
		Description:     toNullString(row.Description),
		Events:          row.Events,
		Expect:          toNullString(row.Expect),
		LastRunAt:       toNullTime(row.LastRunAt),
		LastRunStatus:   toNullString(row.LastRunStatus),
		LastRunResult:   toNullString(row.LastRunResult),
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		Version:         row.Version,
	}
}

// WorkflowScenarioExtra carries fields on WorkflowScenario that are NOT columns — computed
// values, transient state, anything derived rather than stored. WorkflowScenario is
// embedded on WorkflowScenarioExtra (see workflow_scenario_orm.go), so a field added here is a field on
// every WorkflowScenario value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type WorkflowScenarioExtra struct {
}
