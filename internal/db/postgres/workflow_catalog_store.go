package postgres

import (
	"context"
	"database/sql"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type workflowCatalogStore struct{ q pgdb.Querier }

// NewWorkflowCatalogStore creates the Postgres workflow catalog store implementation.
func NewWorkflowCatalogStore(q pgdb.Querier) core.WorkflowCatalogStore {
	return &workflowCatalogStore{q: q}
}

func (s *workflowCatalogStore) CreateWorkflowDraft(ctx context.Context, draft *core.WorkflowDraft) error {
	_, err := s.q.CreateWorkflowDraft(ctx, workflowDraftToCreateParams(draft))
	return err
}

func (s *workflowCatalogStore) UpsertWorkflowDraft(ctx context.Context, draft *core.WorkflowDraft) (*core.WorkflowDraft, error) {
	row, err := s.q.UpsertWorkflowDraft(ctx, workflowDraftToUpsertParams(draft))
	if err != nil {
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

func (s *workflowCatalogStore) GetWorkflowDraft(ctx context.Context, id string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetWorkflowDraft(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

func (s *workflowCatalogStore) GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetWorkflowDraftBySlug(ctx, pgdb.GetWorkflowDraftBySlugParams{UserID: userID, Slug: slug})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

func (s *workflowCatalogStore) GetWorkflowDraftByName(ctx context.Context, userID, name string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetWorkflowDraftByName(ctx, pgdb.GetWorkflowDraftByNameParams{UserID: userID, Lower: name})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

func (s *workflowCatalogStore) GetWorkflowDraftBySourcePath(ctx context.Context, userID, sourcePath string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetWorkflowDraftBySourcePath(ctx, pgdb.GetWorkflowDraftBySourcePathParams{UserID: userID, SourcePath: workflowPtrToNullString(&sourcePath)})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

func (s *workflowCatalogStore) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetUsableWorkflowBySlug(ctx, pgdb.GetUsableWorkflowBySlugParams{UserID: userID, Slug: slug})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

func (s *workflowCatalogStore) ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*core.WorkflowDraft, error) {
	rows, err := s.q.ListWorkflowDraftsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return workflowDraftsFromPG(rows), nil
}

func (s *workflowCatalogStore) UpdateWorkflowDraft(ctx context.Context, draft *core.WorkflowDraft) error {
	_, err := s.q.UpdateWorkflowDraft(ctx, pgdb.UpdateWorkflowDraftParams{
		Name:        draft.Name,
		Slug:        draft.Slug,
		Description: workflowPtrToNullString(draft.Description),
		Definition:  draft.Definition,
		Status:      workflowStatusToPG(draft.Status),
		IsHidden:    draft.IsHidden,
		ID:          draft.ID,
	})
	return err
}

func (s *workflowCatalogStore) UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, status core.WorkflowDraftStatus) error {
	_, err := s.q.UpdateWorkflowDraftDefinition(ctx, pgdb.UpdateWorkflowDraftDefinitionParams{
		Name:       name,
		Slug:       slug,
		Definition: definition,
		Status:     workflowStatusToPG(status),
		ID:         id,
	})
	return err
}

func (s *workflowCatalogStore) SetWorkflowDraftStatus(ctx context.Context, id string, status core.WorkflowDraftStatus) (*core.WorkflowDraft, error) {
	row, err := s.q.SetWorkflowDraftStatus(ctx, pgdb.SetWorkflowDraftStatusParams{Status: workflowStatusToPG(status), ID: id})
	if err != nil {
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

func (s *workflowCatalogStore) SetWorkflowDraftHidden(ctx context.Context, id string, isHidden bool) (*core.WorkflowDraft, error) {
	row, err := s.q.SetWorkflowDraftHidden(ctx, pgdb.SetWorkflowDraftHiddenParams{IsHidden: isHidden, ID: id})
	if err != nil {
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

// DeleteWorkflowDraft returns the deleted row, or sql.ErrNoRows when none matched.
func (s *workflowCatalogStore) DeleteWorkflowDraft(ctx context.Context, id string) (*core.WorkflowDraft, error) {
	row, err := s.q.DeleteWorkflowDraft(ctx, id)
	if err != nil {
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

// DeleteWorkflowDraftBySlug returns the deleted row, or sql.ErrNoRows when none matched.
func (s *workflowCatalogStore) DeleteWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	row, err := s.q.DeleteWorkflowDraftBySlug(ctx, pgdb.DeleteWorkflowDraftBySlugParams{UserID: userID, Slug: slug})
	if err != nil {
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

func (s *workflowCatalogStore) WorkflowSlugExists(ctx context.Context, userID, slug string) (bool, error) {
	return s.q.WorkflowSlugExists(ctx, pgdb.WorkflowSlugExistsParams{UserID: userID, Slug: slug})
}

func (s *workflowCatalogStore) CountWorkflowDraftsByUser(ctx context.Context, userID string) (int64, error) {
	return s.q.CountWorkflowDraftsByUser(ctx, userID)
}

func (s *workflowCatalogStore) UpdateWorkflowForkedFrom(ctx context.Context, draftID string, forkedFrom string) (*core.WorkflowDraft, error) {
	row, err := s.q.UpdateWorkflowForkedFrom(ctx, pgdb.UpdateWorkflowForkedFromParams{ForkedFrom: workflowPtrToNullString(&forkedFrom), ID: draftID})
	if err != nil {
		return nil, err
	}
	return workflowDraftFromPG(workflowDraftRow(row)), nil
}

func (s *workflowCatalogStore) CreateWorkflowScenario(ctx context.Context, scenario *core.WorkflowScenario) error {
	_, err := s.q.CreateWorkflowScenario(ctx, pgdb.CreateWorkflowScenarioParams{
		ID:              scenario.ID,
		WorkflowDraftID: scenario.WorkflowDraftID,
		UserID:          scenario.UserID,
		Name:            scenario.Name,
		Description:     scenario.Description,
		Events:          scenario.Events,
		Expect:          scenario.Expect,
		CreatedAt:       scenario.CreatedAt,
		UpdatedAt:       scenario.UpdatedAt,
	})
	return err
}

func (s *workflowCatalogStore) GetWorkflowScenario(ctx context.Context, id string) (*core.WorkflowScenario, error) {
	row, err := s.q.GetWorkflowScenario(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowScenarioFromPG(row), nil
}

func (s *workflowCatalogStore) GetWorkflowScenarioByName(ctx context.Context, workflowDraftID string, name string) (*core.WorkflowScenario, error) {
	row, err := s.q.GetWorkflowScenarioByName(ctx, pgdb.GetWorkflowScenarioByNameParams{
		WorkflowDraftID: sql.NullString{String: workflowDraftID, Valid: true},
		Name:            name,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowScenarioFromPG(row), nil
}

func (s *workflowCatalogStore) ListWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) ([]*core.WorkflowScenario, error) {
	rows, err := s.q.ListWorkflowScenariosByDraft(ctx, sql.NullString{String: workflowDraftID, Valid: true})
	if err != nil {
		return nil, err
	}
	return workflowScenariosFromPG(rows), nil
}

func (s *workflowCatalogStore) ListWorkflowScenariosByUser(ctx context.Context, userID string) ([]*core.WorkflowScenario, error) {
	rows, err := s.q.ListWorkflowScenariosByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return workflowScenariosFromPG(rows), nil
}

func (s *workflowCatalogStore) UpdateWorkflowScenario(ctx context.Context, scenario *core.WorkflowScenario) error {
	_, err := s.q.UpdateWorkflowScenario(ctx, pgdb.UpdateWorkflowScenarioParams{
		Name:        scenario.Name,
		Description: scenario.Description,
		Events:      scenario.Events,
		Expect:      scenario.Expect,
		ID:          scenario.ID,
	})
	return err
}

func (s *workflowCatalogStore) UpdateWorkflowScenarioResult(ctx context.Context, id string, status string, result string) error {
	_, err := s.q.UpdateWorkflowScenarioResult(ctx, pgdb.UpdateWorkflowScenarioResultParams{
		LastRunStatus: sql.NullString{String: status, Valid: true},
		LastRunResult: sql.NullString{String: result, Valid: true},
		ID:            id,
	})
	return err
}

func (s *workflowCatalogStore) DeleteWorkflowScenario(ctx context.Context, id string) error {
	return s.q.DeleteWorkflowScenario(ctx, id)
}

func (s *workflowCatalogStore) DeleteWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) error {
	return s.q.DeleteWorkflowScenariosByDraft(ctx, sql.NullString{String: workflowDraftID, Valid: true})
}

// workflowDraftRow is the explicit-column projection every workflow_drafts
// query returns (chat_id is retired and never selected); sqlc emits one
// identically-shaped Row type per query, converted to this one.
type workflowDraftRow = pgdb.GetWorkflowDraftRow

func workflowDraftFromPG(sw workflowDraftRow) *core.WorkflowDraft {
	return &core.WorkflowDraft{
		ID:          sw.ID,
		UserID:      sw.UserID,
		Name:        sw.Name,
		Slug:        sw.Slug,
		Description: workflowNullStringToPtr(sw.Description),
		Definition:  sw.Definition,
		Status:      core.WorkflowDraftStatus(sw.Status),
		SourcePath:  workflowNullStringToPtr(sw.SourcePath),
		ForkedFrom:  workflowNullStringToPtr(sw.ForkedFrom),
		CreatedAt:   sw.CreatedAt,
		UpdatedAt:   sw.UpdatedAt,
		IsHidden:    sw.IsHidden,
		Version:     sw.Version,
	}
}

func workflowDraftsFromPG(rows []pgdb.ListWorkflowDraftsByUserRow) []*core.WorkflowDraft {
	drafts := make([]*core.WorkflowDraft, len(rows))
	for i, row := range rows {
		drafts[i] = workflowDraftFromPG(workflowDraftRow(row))
	}
	return drafts
}

func workflowDraftToCreateParams(draft *core.WorkflowDraft) pgdb.CreateWorkflowDraftParams {
	return pgdb.CreateWorkflowDraftParams{
		ID:          draft.ID,
		UserID:      draft.UserID,
		Name:        draft.Name,
		Slug:        draft.Slug,
		Description: workflowPtrToNullString(draft.Description),
		Definition:  draft.Definition,
		Status:      workflowStatusToPG(draft.Status),
		SourcePath:  workflowPtrToNullString(draft.SourcePath),
		ForkedFrom:  workflowPtrToNullString(draft.ForkedFrom),
		CreatedAt:   draft.CreatedAt,
		UpdatedAt:   draft.UpdatedAt,
		IsHidden:    draft.IsHidden,
	}
}

func workflowDraftToUpsertParams(draft *core.WorkflowDraft) pgdb.UpsertWorkflowDraftParams {
	return pgdb.UpsertWorkflowDraftParams{
		ID:          draft.ID,
		UserID:      draft.UserID,
		Name:        draft.Name,
		Slug:        draft.Slug,
		Description: workflowPtrToNullString(draft.Description),
		Definition:  draft.Definition,
		Status:      workflowStatusToPG(draft.Status),
		SourcePath:  workflowPtrToNullString(draft.SourcePath),
		ForkedFrom:  workflowPtrToNullString(draft.ForkedFrom),
		CreatedAt:   draft.CreatedAt,
		UpdatedAt:   draft.UpdatedAt,
		IsHidden:    draft.IsHidden,
	}
}

func workflowScenarioFromPG(row pgdb.WorkflowScenario) *core.WorkflowScenario {
	return &core.WorkflowScenario{
		ID:              row.ID,
		WorkflowDraftID: row.WorkflowDraftID,
		UserID:          row.UserID,
		Name:            row.Name,
		Description:     row.Description,
		Events:          row.Events,
		Expect:          row.Expect,
		LastRunAt:       row.LastRunAt,
		LastRunStatus:   row.LastRunStatus,
		LastRunResult:   row.LastRunResult,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func workflowScenariosFromPG(rows []pgdb.WorkflowScenario) []*core.WorkflowScenario {
	result := make([]*core.WorkflowScenario, len(rows))
	for i, row := range rows {
		result[i] = workflowScenarioFromPG(row)
	}
	return result
}

// workflowStatusToPG maps an unset status to draft — the safe default: an
// unset status must never make a workflow runnable.
func workflowStatusToPG(status core.WorkflowDraftStatus) string {
	if status == "" {
		return string(core.WorkflowDraftStatusDraft)
	}
	return string(status)
}

func workflowPtrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func workflowNullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}
