package postgres

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// workflowStore is core.WorkflowStore. The workflows, workflow_checkpoints
// and step_executions halves run on the forge ORM (internal/store's
// workflow_repo_ext.go, workflow_checkpoint_repo_ext.go and
// step_execution_repo_ext.go); every call joins the transaction ctx carries.
type workflowStore struct {
	db orm.Context
}

// NewWorkflowStore creates the workflow store implementation.
func NewWorkflowStore(db orm.Context) core.WorkflowStore {
	return &workflowStore{db: db}
}

func (s *workflowStore) CreateWorkflow(ctx context.Context, workflow *core.Workflow) error {
	return store.InsertWorkflowIfAbsent(ctx, s.db, workflow)
}

func (s *workflowStore) GetWorkflow(ctx context.Context, id string) (*core.Workflow, error) {
	return store.LoadWorkflow(ctx, s.db, id)
}

func (s *workflowStore) GetWorkflowByThread(ctx context.Context, chatID, thread string) (*core.Workflow, error) {
	return store.FindWorkflowByThread(ctx, s.db, chatID, thread)
}

func (s *workflowStore) ListWorkflowsByChat(ctx context.Context, chatID string) ([]*core.Workflow, error) {
	return store.ListChatWorkflows(ctx, s.db, chatID)
}

func (s *workflowStore) ListChildWorkflows(ctx context.Context, parentID string) ([]*core.Workflow, error) {
	return store.ListChildWorkflows(ctx, s.db, parentID)
}

func (s *workflowStore) ListRootWorkflows(ctx context.Context, chatID string) ([]*core.Workflow, error) {
	return store.ListRootWorkflows(ctx, s.db, chatID)
}

func (s *workflowStore) GetRootWorkflowStatusForChats(ctx context.Context, chatIDs []string) (map[string]core.WorkflowStatus, error) {
	return store.RootWorkflowStatusForChats(ctx, s.db, chatIDs)
}

func (s *workflowStore) CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus core.WorkflowStatus) (bool, error) {
	return store.CompareAndSwapWorkflowStatus(ctx, s.db, id, newStatus, expectedStatus)
}

func (s *workflowStore) UpdateWorkflowStatus(ctx context.Context, id string, status core.WorkflowStatus) error {
	return store.UpdateWorkflowStatus(ctx, s.db, id, status)
}

func (s *workflowStore) SetWorkflowOutcome(ctx context.Context, id string, outcome string) error {
	return store.SetWorkflowOutcome(ctx, s.db, id, outcome)
}

func (s *workflowStore) UpdateWorkflowName(ctx context.Context, id string, workflowName string) error {
	return store.RenamePendingWorkflow(ctx, s.db, id, workflowName)
}

func (s *workflowStore) CascadeTerminalStatusToDescendants(ctx context.Context, parentWorkflowID string, reason core.WorkflowStopReason) error {
	return store.CascadeTerminalStatusToDescendants(ctx, s.db, parentWorkflowID, reason)
}

func (s *workflowStore) ReapOrphanedWorkflowDescendants(ctx context.Context) ([]core.ReapedWorkflow, error) {
	return store.ReapOrphanedWorkflowDescendants(ctx, s.db)
}

func (s *workflowStore) ReviveSubtreeLiveAt(ctx context.Context, rootWorkflowID string, at time.Time) (workflowsRevived, threadsRevived int64, err error) {
	return store.ReviveSubtreeLiveAt(ctx, s.db, rootWorkflowID, at)
}

func (s *workflowStore) PauseRunningWorkflowsByChat(ctx context.Context, chatID string) error {
	return store.PauseRunningWorkflowsByChat(ctx, s.db, chatID)
}

func (s *workflowStore) ResumeWorkflowsByChat(ctx context.Context, chatID string) error {
	return store.ResumeWorkflowsByChat(ctx, s.db, chatID)
}

func (s *workflowStore) DeleteWorkflow(ctx context.Context, id string) error {
	return store.RemoveWorkflow(ctx, s.db, id)
}

func (s *workflowStore) DeleteWorkflowsByChat(ctx context.Context, chatID string) error {
	return store.RemoveChatWorkflows(ctx, s.db, chatID)
}

func (s *workflowStore) ListWorkflowsByStatus(ctx context.Context, status core.WorkflowStatus) ([]*core.Workflow, error) {
	return store.ListWorkflowsByStatus(ctx, s.db, status)
}

func (s *workflowStore) ListRootWorkflowsByStatus(ctx context.Context, status core.WorkflowStatus) ([]*core.Workflow, error) {
	return store.ListRootWorkflowsByStatus(ctx, s.db, status)
}

func (s *workflowStore) UpdateWorkflowWorkerStarted(ctx context.Context, workflowID string) error {
	return store.MarkWorkflowWorkerStarted(ctx, s.db, workflowID)
}

func (s *workflowStore) UpdateWorkflowWorkerStopped(ctx context.Context, workflowID string) error {
	return store.MarkWorkflowWorkerStopped(ctx, s.db, workflowID)
}

func (s *workflowStore) UpsertWorkflowCheckpoint(ctx context.Context, checkpoint *core.WorkflowCheckpoint) error {
	return store.SaveWorkflowCheckpoint(ctx, s.db, checkpoint)
}

func (s *workflowStore) GetWorkflowCheckpoint(ctx context.Context, workflowID string) (*core.WorkflowCheckpoint, error) {
	return store.LoadWorkflowCheckpoint(ctx, s.db, workflowID)
}

func (s *workflowStore) DeleteWorkflowCheckpoint(ctx context.Context, workflowID string) error {
	return store.ClearWorkflowCheckpoint(ctx, s.db, workflowID)
}

func (s *workflowStore) CreateStepExecution(ctx context.Context, exec *core.StepExecution) error {
	return store.InsertStepExecution(ctx, s.db, exec)
}

func (s *workflowStore) ListStepExecutionRecordsForChat(ctx context.Context, chatID, nodePath string, limit int) ([]*core.StepExecution, error) {
	return store.ListStepExecutionRecordsForChat(ctx, s.db, chatID, nodePath, limit)
}

func (s *workflowStore) GetStepExecution(ctx context.Context, id string) (*core.StepExecution, error) {
	return store.LoadStepExecution(ctx, s.db, id)
}

func (s *workflowStore) GetStepExecutionsByWorkflow(ctx context.Context, workflowID string) ([]*core.StepExecution, error) {
	return store.ListWorkflowStepExecutions(ctx, s.db, workflowID)
}

// GetStepExecutionsForChat is the FULL view: output only for user-facing
// activities. model.InternalActivities crosses into SQL here and only here.
func (s *workflowStore) GetStepExecutionsForChat(ctx context.Context, chatID string) ([]*core.ChatStepExecution, error) {
	return store.ListChatStepExecutions(ctx, s.db, chatID, model.InternalActivities)
}

func (s *workflowStore) GetBasicStepExecutionsForChat(ctx context.Context, chatID string) ([]*core.ChatStepExecution, error) {
	return store.ListBasicChatStepExecutions(ctx, s.db, chatID)
}

func (s *workflowStore) GetStepExecutionsByStep(ctx context.Context, workflowID, stepID string) ([]*core.StepExecution, error) {
	return store.ListStepExecutionsByStep(ctx, s.db, workflowID, stepID)
}

func (s *workflowStore) DeleteStepExecutionsByWorkflow(ctx context.Context, workflowID string) error {
	return store.RemoveWorkflowStepExecutions(ctx, s.db, workflowID)
}
