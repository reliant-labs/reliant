package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

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
	return workflowDraftFromPG(row), nil
}

func (s *workflowCatalogStore) GetWorkflowDraft(ctx context.Context, id string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetWorkflowDraft(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	return workflowDraftFromPG(row), nil
}

func (s *workflowCatalogStore) GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetWorkflowDraftBySlug(ctx, pgdb.GetWorkflowDraftBySlugParams{UserID: userID, Slug: slug})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowDraftFromPG(row), nil
}

func (s *workflowCatalogStore) GetWorkflowDraftByName(ctx context.Context, userID, name string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetWorkflowDraftByName(ctx, pgdb.GetWorkflowDraftByNameParams{UserID: userID, Lower: name})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowDraftFromPG(row), nil
}

func (s *workflowCatalogStore) GetWorkflowDraftByChatID(ctx context.Context, chatID string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetWorkflowDraftByChatID(ctx, workflowPtrToNullString(&chatID))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowDraftFromPG(row), nil
}

func (s *workflowCatalogStore) GetWorkflowDraftBySourcePath(ctx context.Context, userID, sourcePath string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetWorkflowDraftBySourcePath(ctx, pgdb.GetWorkflowDraftBySourcePathParams{UserID: userID, SourcePath: workflowPtrToNullString(&sourcePath)})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowDraftFromPG(row), nil
}

func (s *workflowCatalogStore) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	row, err := s.q.GetUsableWorkflowBySlug(ctx, pgdb.GetUsableWorkflowBySlugParams{UserID: userID, Slug: slug})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowDraftFromPG(row), nil
}

func (s *workflowCatalogStore) ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*core.WorkflowDraft, error) {
	rows, err := s.q.ListWorkflowDraftsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return workflowDraftsFromPG(rows), nil
}

func (s *workflowCatalogStore) ListUsableWorkflowsByUser(ctx context.Context, userID string) ([]*core.WorkflowDraft, error) {
	rows, err := s.q.ListUsableWorkflowsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return workflowDraftsFromPG(rows), nil
}

func (s *workflowCatalogStore) UpdateWorkflowDraft(ctx context.Context, draft *core.WorkflowDraft) error {
	var isValid int64
	if draft.IsValid {
		isValid = 1
	}
	_, err := s.q.UpdateWorkflowDraft(ctx, pgdb.UpdateWorkflowDraftParams{
		Name:             draft.Name,
		Slug:             draft.Slug,
		Description:      workflowPtrToNullString(draft.Description),
		Definition:       draft.Definition,
		IsValid:          isValid,
		ValidationErrors: workflowPtrToNullString(draft.ValidationErrors),
		IsHidden:         draft.IsHidden,
		ID:               draft.ID,
	})
	return err
}

func (s *workflowCatalogStore) UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, isValid bool, validationErrors *string) error {
	var isValidInt int64
	if isValid {
		isValidInt = 1
	}
	_, err := s.q.UpdateWorkflowDraftDefinition(ctx, pgdb.UpdateWorkflowDraftDefinitionParams{
		Name:             name,
		Slug:             slug,
		Definition:       definition,
		IsValid:          isValidInt,
		ValidationErrors: workflowPtrToNullString(validationErrors),
		ID:               id,
	})
	return err
}

func (s *workflowCatalogStore) SetWorkflowDraftHidden(ctx context.Context, id string, isHidden bool) (*core.WorkflowDraft, error) {
	row, err := s.q.SetWorkflowDraftHidden(ctx, pgdb.SetWorkflowDraftHiddenParams{IsHidden: isHidden, ID: id})
	if err != nil {
		return nil, err
	}
	return workflowDraftFromPG(row), nil
}

func (s *workflowCatalogStore) DeleteWorkflowDraft(ctx context.Context, id string) error {
	return s.q.DeleteWorkflowDraft(ctx, id)
}

func (s *workflowCatalogStore) DeleteWorkflowDraftBySlug(ctx context.Context, userID, slug string) error {
	return s.q.DeleteWorkflowDraftBySlug(ctx, pgdb.DeleteWorkflowDraftBySlugParams{UserID: userID, Slug: slug})
}

func (s *workflowCatalogStore) WorkflowSlugExists(ctx context.Context, userID, slug string) (bool, error) {
	return s.q.WorkflowSlugExists(ctx, pgdb.WorkflowSlugExistsParams{UserID: userID, Slug: slug})
}

func (s *workflowCatalogStore) CountWorkflowDraftsByUser(ctx context.Context, userID string) (int64, error) {
	return s.q.CountWorkflowDraftsByUser(ctx, userID)
}

func (s *workflowCatalogStore) AssociateChatWithDraft(ctx context.Context, draftID string, chatID string) (*core.WorkflowDraft, error) {
	row, err := s.q.AssociateChatWithDraft(ctx, pgdb.AssociateChatWithDraftParams{ChatID: workflowPtrToNullString(&chatID), ID: draftID})
	if err != nil {
		return nil, err
	}
	return workflowDraftFromPG(row), nil
}

func (s *workflowCatalogStore) UpdateWorkflowForkedFrom(ctx context.Context, draftID string, forkedFrom string) (*core.WorkflowDraft, error) {
	row, err := s.q.UpdateWorkflowForkedFrom(ctx, pgdb.UpdateWorkflowForkedFromParams{ForkedFrom: workflowPtrToNullString(&forkedFrom), ID: draftID})
	if err != nil {
		return nil, err
	}
	return workflowDraftFromPG(row), nil
}

func (s *workflowCatalogStore) CreatePreset(ctx context.Context, preset *core.Preset) (*core.Preset, error) {
	params, err := presetToCreateParams(preset)
	if err != nil {
		return nil, err
	}
	row, err := s.q.CreatePreset(ctx, params)
	if err != nil {
		return nil, err
	}
	return presetFromPG(row), nil
}

func (s *workflowCatalogStore) UpsertPreset(ctx context.Context, preset *core.Preset) (*core.Preset, error) {
	params, err := presetToUpsertParams(preset)
	if err != nil {
		return nil, err
	}
	row, err := s.q.UpsertPreset(ctx, params)
	if err != nil {
		return nil, err
	}
	return presetFromPG(row), nil
}

func (s *workflowCatalogStore) GetPreset(ctx context.Context, id string) (*core.Preset, error) {
	row, err := s.q.GetPreset(ctx, id)
	if err != nil {
		return nil, err
	}
	return presetFromPG(row), nil
}

func (s *workflowCatalogStore) GetPresetBySlug(ctx context.Context, userID, slug string) (*core.Preset, error) {
	row, err := s.q.GetPresetBySlug(ctx, pgdb.GetPresetBySlugParams{UserID: userID, Slug: slug})
	if err != nil {
		return nil, err
	}
	return presetFromPG(row), nil
}

func (s *workflowCatalogStore) GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*core.Preset, error) {
	row, err := s.q.GetPresetBySlugAndProject(ctx, pgdb.GetPresetBySlugAndProjectParams{UserID: userID, Slug: slug, ProjectID: workflowPtrToNullString(&projectID)})
	if err != nil {
		return nil, err
	}
	return presetFromPG(row), nil
}

func (s *workflowCatalogStore) ListUserPresets(ctx context.Context, userID string) ([]*core.Preset, error) {
	rows, err := s.q.ListUserPresets(ctx, userID)
	if err != nil {
		return nil, err
	}
	return presetsFromPG(rows), nil
}

func (s *workflowCatalogStore) ListUserPresetsGlobal(ctx context.Context, userID string) ([]*core.Preset, error) {
	rows, err := s.q.ListUserPresetsGlobal(ctx, userID)
	if err != nil {
		return nil, err
	}
	return presetsFromPG(rows), nil
}

func (s *workflowCatalogStore) ListUserPresetsByProject(ctx context.Context, userID, projectID string) ([]*core.Preset, error) {
	rows, err := s.q.ListUserPresetsByProject(ctx, pgdb.ListUserPresetsByProjectParams{UserID: userID, ProjectID: workflowPtrToNullString(&projectID)})
	if err != nil {
		return nil, err
	}
	return presetsFromPG(rows), nil
}

func (s *workflowCatalogStore) ListPresetsByTag(ctx context.Context, userID, tag, projectID string) ([]*core.Preset, error) {
	rows, err := s.q.ListPresetsByTag(ctx, pgdb.ListPresetsByTagParams{UserID: userID, Tag: tag, ProjectID: workflowPtrToNullString(&projectID)})
	if err != nil {
		return nil, err
	}
	return presetsFromPG(rows), nil
}

func (s *workflowCatalogStore) UpdatePreset(ctx context.Context, preset *core.Preset) (*core.Preset, error) {
	params, err := presetToUpdateParams(preset)
	if err != nil {
		return nil, err
	}
	row, err := s.q.UpdatePreset(ctx, params)
	if err != nil {
		return nil, err
	}
	return presetFromPG(row), nil
}

func (s *workflowCatalogStore) DeletePreset(ctx context.Context, id string) error {
	return s.q.DeletePreset(ctx, id)
}

func (s *workflowCatalogStore) DeletePresetBySlug(ctx context.Context, userID, slug string) error {
	return s.q.DeletePresetBySlug(ctx, pgdb.DeletePresetBySlugParams{UserID: userID, Slug: slug})
}

func (s *workflowCatalogStore) DeletePresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) error {
	return s.q.DeletePresetBySlugAndProject(ctx, pgdb.DeletePresetBySlugAndProjectParams{UserID: userID, Slug: slug, ProjectID: workflowPtrToNullString(&projectID)})
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

func workflowDraftFromPG(sw pgdb.WorkflowDraft) *core.WorkflowDraft {
	return &core.WorkflowDraft{
		ID:               sw.ID,
		UserID:           sw.UserID,
		Name:             sw.Name,
		Slug:             sw.Slug,
		Description:      workflowNullStringToPtr(sw.Description),
		Definition:       sw.Definition,
		IsValid:          sw.IsValid != 0,
		ValidationErrors: workflowNullStringToPtr(sw.ValidationErrors),
		SourcePath:       workflowNullStringToPtr(sw.SourcePath),
		ForkedFrom:       workflowNullStringToPtr(sw.ForkedFrom),
		ChatID:           workflowNullStringToPtr(sw.ChatID),
		CreatedAt:        sw.CreatedAt,
		UpdatedAt:        sw.UpdatedAt,
		IsHidden:         sw.IsHidden,
		Version:          sw.Version,
	}
}

func workflowDraftsFromPG(rows []pgdb.WorkflowDraft) []*core.WorkflowDraft {
	drafts := make([]*core.WorkflowDraft, len(rows))
	for i, row := range rows {
		drafts[i] = workflowDraftFromPG(row)
	}
	return drafts
}

func workflowDraftToCreateParams(draft *core.WorkflowDraft) pgdb.CreateWorkflowDraftParams {
	var isValid int64
	if draft.IsValid {
		isValid = 1
	}

	return pgdb.CreateWorkflowDraftParams{
		ID:               draft.ID,
		UserID:           draft.UserID,
		Name:             draft.Name,
		Slug:             draft.Slug,
		Description:      workflowPtrToNullString(draft.Description),
		Definition:       draft.Definition,
		IsValid:          isValid,
		ValidationErrors: workflowPtrToNullString(draft.ValidationErrors),
		SourcePath:       workflowPtrToNullString(draft.SourcePath),
		ForkedFrom:       workflowPtrToNullString(draft.ForkedFrom),
		ChatID:           workflowPtrToNullString(draft.ChatID),
		CreatedAt:        draft.CreatedAt,
		UpdatedAt:        draft.UpdatedAt,
		IsHidden:         draft.IsHidden,
	}
}

func workflowDraftToUpsertParams(draft *core.WorkflowDraft) pgdb.UpsertWorkflowDraftParams {
	var isValid int64
	if draft.IsValid {
		isValid = 1
	}

	return pgdb.UpsertWorkflowDraftParams{
		ID:               draft.ID,
		UserID:           draft.UserID,
		Name:             draft.Name,
		Slug:             draft.Slug,
		Description:      workflowPtrToNullString(draft.Description),
		Definition:       draft.Definition,
		IsValid:          isValid,
		ValidationErrors: workflowPtrToNullString(draft.ValidationErrors),
		SourcePath:       workflowPtrToNullString(draft.SourcePath),
		ForkedFrom:       workflowPtrToNullString(draft.ForkedFrom),
		ChatID:           workflowPtrToNullString(draft.ChatID),
		CreatedAt:        draft.CreatedAt,
		UpdatedAt:        draft.UpdatedAt,
		IsHidden:         draft.IsHidden,
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

func presetFromPG(sp pgdb.Preset) *core.Preset {
	params := make(map[string]interface{})
	if sp.Params != "" {
		_ = json.Unmarshal([]byte(sp.Params), &params)
	}

	return &core.Preset{
		ID:          sp.ID,
		UserID:      sp.UserID,
		ProjectID:   workflowNullStringToPtr(sp.ProjectID),
		Name:        sp.Name,
		Slug:        sp.Slug,
		Description: workflowNullStringToPtr(sp.Description),
		Tag:         sp.Tag,
		Params:      params,
		CreatedAt:   sp.CreatedAt,
		UpdatedAt:   sp.UpdatedAt,
	}
}

func presetsFromPG(rows []pgdb.Preset) []*core.Preset {
	presets := make([]*core.Preset, len(rows))
	for i, row := range rows {
		presets[i] = presetFromPG(row)
	}
	return presets
}

func presetToCreateParams(preset *core.Preset) (pgdb.CreatePresetParams, error) {
	paramsJSON, err := json.Marshal(preset.Params)
	if err != nil {
		return pgdb.CreatePresetParams{}, fmt.Errorf("failed to marshal preset params: %w", err)
	}
	return pgdb.CreatePresetParams{
		ID:          preset.ID,
		UserID:      preset.UserID,
		ProjectID:   workflowPtrToNullString(preset.ProjectID),
		Name:        preset.Name,
		Slug:        preset.Slug,
		Description: workflowPtrToNullString(preset.Description),
		Tag:         preset.Tag,
		Params:      string(paramsJSON),
	}, nil
}

func presetToUpsertParams(preset *core.Preset) (pgdb.UpsertPresetParams, error) {
	paramsJSON, err := json.Marshal(preset.Params)
	if err != nil {
		return pgdb.UpsertPresetParams{}, fmt.Errorf("failed to marshal preset params: %w", err)
	}
	return pgdb.UpsertPresetParams{
		ID:          preset.ID,
		UserID:      preset.UserID,
		ProjectID:   workflowPtrToNullString(preset.ProjectID),
		Name:        preset.Name,
		Slug:        preset.Slug,
		Description: workflowPtrToNullString(preset.Description),
		Tag:         preset.Tag,
		Params:      string(paramsJSON),
	}, nil
}

func presetToUpdateParams(preset *core.Preset) (pgdb.UpdatePresetParams, error) {
	paramsJSON, err := json.Marshal(preset.Params)
	if err != nil {
		return pgdb.UpdatePresetParams{}, fmt.Errorf("failed to marshal preset params: %w", err)
	}
	return pgdb.UpdatePresetParams{
		Name:        preset.Name,
		Description: workflowPtrToNullString(preset.Description),
		Tag:         preset.Tag,
		Params:      string(paramsJSON),
		ID:          preset.ID,
	}, nil
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
