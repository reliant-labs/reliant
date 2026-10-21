package postgres

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// workflowCatalogStore is core.WorkflowCatalogStore over the forge ORM
// (internal/store's workflow_draft_repo_ext.go and
// workflow_scenario_repo_ext.go). Every call joins the transaction ctx
// carries.
type workflowCatalogStore struct{ db orm.Context }

// NewWorkflowCatalogStore creates the workflow catalog store implementation.
func NewWorkflowCatalogStore(db orm.Context) core.WorkflowCatalogStore {
	return &workflowCatalogStore{db: db}
}

func (s *workflowCatalogStore) CreateWorkflowDraft(ctx context.Context, draft *core.WorkflowDraft) error {
	return store.InsertWorkflowDraft(ctx, s.db, draft)
}

func (s *workflowCatalogStore) UpsertWorkflowDraft(ctx context.Context, draft *core.WorkflowDraft) (*core.WorkflowDraft, error) {
	return store.UpsertWorkflowDraftBySlug(ctx, s.db, draft)
}

func (s *workflowCatalogStore) GetWorkflowDraft(ctx context.Context, id string) (*core.WorkflowDraft, error) {
	return store.LoadWorkflowDraft(ctx, s.db, id)
}

func (s *workflowCatalogStore) GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	return store.FindWorkflowDraftBySlug(ctx, s.db, userID, slug)
}

func (s *workflowCatalogStore) GetWorkflowDraftByName(ctx context.Context, userID, name string) (*core.WorkflowDraft, error) {
	return store.FindWorkflowDraftByName(ctx, s.db, userID, name)
}

func (s *workflowCatalogStore) GetWorkflowDraftBySourcePath(ctx context.Context, userID, sourcePath string) (*core.WorkflowDraft, error) {
	return store.FindWorkflowDraftBySourcePath(ctx, s.db, userID, sourcePath)
}

func (s *workflowCatalogStore) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	return store.FindUsableWorkflowBySlug(ctx, s.db, userID, slug)
}

func (s *workflowCatalogStore) ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*core.WorkflowDraft, error) {
	return store.ListUserWorkflowDrafts(ctx, s.db, userID)
}

func (s *workflowCatalogStore) UpdateWorkflowDraft(ctx context.Context, draft *core.WorkflowDraft) error {
	return store.ReplaceWorkflowDraft(ctx, s.db, draft)
}

func (s *workflowCatalogStore) UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, status core.WorkflowDraftStatus) error {
	return store.SetWorkflowDraftDefinition(ctx, s.db, id, name, slug, definition, status)
}

func (s *workflowCatalogStore) SetWorkflowDraftStatus(ctx context.Context, id string, status core.WorkflowDraftStatus) (*core.WorkflowDraft, error) {
	return store.SetWorkflowDraftStatus(ctx, s.db, id, status)
}

func (s *workflowCatalogStore) SetWorkflowDraftHidden(ctx context.Context, id string, isHidden bool) (*core.WorkflowDraft, error) {
	return store.SetWorkflowDraftHidden(ctx, s.db, id, isHidden)
}

// DeleteWorkflowDraft returns the deleted row, or sql.ErrNoRows when none matched.
func (s *workflowCatalogStore) DeleteWorkflowDraft(ctx context.Context, id string) (*core.WorkflowDraft, error) {
	return store.RemoveWorkflowDraft(ctx, s.db, id)
}

// DeleteWorkflowDraftBySlug returns the deleted row, or sql.ErrNoRows when none matched.
func (s *workflowCatalogStore) DeleteWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	return store.RemoveWorkflowDraftBySlug(ctx, s.db, userID, slug)
}

func (s *workflowCatalogStore) WorkflowSlugExists(ctx context.Context, userID, slug string) (bool, error) {
	return store.WorkflowSlugExists(ctx, s.db, userID, slug)
}

func (s *workflowCatalogStore) CountWorkflowDraftsByUser(ctx context.Context, userID string) (int64, error) {
	return store.CountUserWorkflowDrafts(ctx, s.db, userID)
}

func (s *workflowCatalogStore) UpdateWorkflowForkedFrom(ctx context.Context, draftID string, forkedFrom string) (*core.WorkflowDraft, error) {
	return store.SetWorkflowDraftForkedFrom(ctx, s.db, draftID, forkedFrom)
}

func (s *workflowCatalogStore) CreateWorkflowScenario(ctx context.Context, scenario *core.WorkflowScenario) error {
	return store.InsertWorkflowScenario(ctx, s.db, scenario)
}

func (s *workflowCatalogStore) GetWorkflowScenario(ctx context.Context, id string) (*core.WorkflowScenario, error) {
	return store.FindWorkflowScenario(ctx, s.db, id)
}

func (s *workflowCatalogStore) GetWorkflowScenarioByName(ctx context.Context, workflowDraftID string, name string) (*core.WorkflowScenario, error) {
	return store.FindWorkflowScenarioByName(ctx, s.db, workflowDraftID, name)
}

func (s *workflowCatalogStore) ListWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) ([]*core.WorkflowScenario, error) {
	return store.ListDraftWorkflowScenarios(ctx, s.db, workflowDraftID)
}

func (s *workflowCatalogStore) ListWorkflowScenariosByUser(ctx context.Context, userID string) ([]*core.WorkflowScenario, error) {
	return store.ListUserWorkflowScenarios(ctx, s.db, userID)
}

func (s *workflowCatalogStore) UpdateWorkflowScenario(ctx context.Context, scenario *core.WorkflowScenario) error {
	return store.ReplaceWorkflowScenario(ctx, s.db, scenario)
}

func (s *workflowCatalogStore) UpdateWorkflowScenarioResult(ctx context.Context, id string, status string, result string) error {
	return store.RecordWorkflowScenarioResult(ctx, s.db, id, status, result)
}

func (s *workflowCatalogStore) DeleteWorkflowScenario(ctx context.Context, id string) error {
	return store.RemoveWorkflowScenario(ctx, s.db, id)
}

func (s *workflowCatalogStore) DeleteWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) error {
	return store.RemoveDraftWorkflowScenarios(ctx, s.db, workflowDraftID)
}
