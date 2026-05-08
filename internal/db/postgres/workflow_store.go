package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type workflowStore struct{ q pgdb.Querier }

// NewWorkflowStore creates the Postgres workflow store implementation.
func NewWorkflowStore(q pgdb.Querier) core.WorkflowStore { return &workflowStore{q: q} }

func (s *workflowStore) CreateWorkflow(ctx context.Context, workflow *core.Workflow) error {
	_, err := s.q.CreateWorkflow(ctx, pgdb.CreateWorkflowParams{
		ID:              workflow.ID,
		ParentID:        ptrToNullString(workflow.ParentID),
		ChatID:          workflow.ChatID,
		WorkflowName:    workflow.WorkflowName,
		Thread:          workflow.Thread,
		Status:          int32(workflow.Status),
		SpawnedByNodeID: ptrToNullString(workflow.SpawnedByNodeID),
		LoopIteration:   workflowPtrToNullInt64(workflow.LoopIteration),
		CreatedAt:       workflow.CreatedAt,
		CompletedAt:     workflowPtrToNullTime(workflow.CompletedAt),
	})
	return err
}

func (s *workflowStore) GetWorkflow(ctx context.Context, id string) (*core.Workflow, error) {
	row, err := s.q.GetWorkflow(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("workflow not found: %s", id)
		}
		return nil, err
	}
	return workflowFromPG(row), nil
}

func (s *workflowStore) GetWorkflowByThread(ctx context.Context, chatID, thread string) (*core.Workflow, error) {
	row, err := s.q.GetWorkflowByThread(ctx, pgdb.GetWorkflowByThreadParams{ChatID: chatID, Thread: thread})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return workflowFromPG(row), nil
}

func (s *workflowStore) ListWorkflowsByChat(ctx context.Context, chatID string) ([]*core.Workflow, error) {
	rows, err := s.q.ListWorkflowsByChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	return workflowsFromPG(rows), nil
}

func (s *workflowStore) ListChildWorkflows(ctx context.Context, parentID string) ([]*core.Workflow, error) {
	rows, err := s.q.ListChildWorkflows(ctx, sql.NullString{String: parentID, Valid: true})
	if err != nil {
		return nil, err
	}
	return workflowsFromPG(rows), nil
}

func (s *workflowStore) ListRootWorkflows(ctx context.Context, chatID string) ([]*core.Workflow, error) {
	rows, err := s.q.ListRootWorkflows(ctx, chatID)
	if err != nil {
		return nil, err
	}
	return workflowsFromPG(rows), nil
}

func (s *workflowStore) GetRootWorkflowStatusForChats(ctx context.Context, chatIDs []string) (map[string]core.WorkflowStatus, error) {
	result := make(map[string]core.WorkflowStatus, len(chatIDs))
	for _, chatID := range chatIDs {
		row, err := s.q.GetRootWorkflowStatusForChat(ctx, chatID)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, err
		}
		if status, ok := row.Status.(int32); ok {
			result[row.ChatID] = core.WorkflowStatus(status)
		} else if status, ok := row.Status.(int64); ok {
			result[row.ChatID] = core.WorkflowStatus(int32(status))
		}
	}
	return result, nil
}

func (s *workflowStore) CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus core.WorkflowStatus) (bool, error) {
	_, err := s.q.CompareAndSwapWorkflowStatus(ctx, pgdb.CompareAndSwapWorkflowStatusParams{
		Status:   int32(newStatus),
		ID:       id,
		Status_2: int32(expectedStatus),
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *workflowStore) UpdateWorkflowStatus(ctx context.Context, id string, status core.WorkflowStatus) error {
	_, err := s.q.UpdateWorkflowStatus(ctx, pgdb.UpdateWorkflowStatusParams{Status: int32(status), ID: id})
	return err
}

func (s *workflowStore) UpdateWorkflowName(ctx context.Context, id string, workflowName string) error {
	_, err := s.q.UpdateWorkflowName(ctx, pgdb.UpdateWorkflowNameParams{WorkflowName: workflowName, ID: id})
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("cannot update workflow name: workflow not found or status is not pending")
		}
		return err
	}
	return nil
}

func (s *workflowStore) CompleteChildWorkflows(ctx context.Context, parentWorkflowID string) error {
	return s.q.CompleteChildWorkflows(ctx, sql.NullString{String: parentWorkflowID, Valid: true})
}

func (s *workflowStore) PauseRunningWorkflowsByChat(ctx context.Context, chatID string) error {
	return s.q.PauseRunningWorkflowsByChat(ctx, chatID)
}

func (s *workflowStore) ResumeWorkflowsByChat(ctx context.Context, chatID string) error {
	return s.q.ResumeWorkflowsByChat(ctx, chatID)
}

func (s *workflowStore) DeleteWorkflow(ctx context.Context, id string) error {
	return s.q.DeleteWorkflow(ctx, id)
}

func (s *workflowStore) DeleteWorkflowsByChat(ctx context.Context, chatID string) error {
	return s.q.DeleteWorkflowsByChat(ctx, chatID)
}

func (s *workflowStore) ListWorkflowsByStatus(ctx context.Context, status core.WorkflowStatus) ([]*core.Workflow, error) {
	rows, err := s.q.ListWorkflowsByStatus(ctx, int32(status))
	if err != nil {
		return nil, err
	}
	return workflowsFromPG(rows), nil
}

func (s *workflowStore) ListRootWorkflowsByStatus(ctx context.Context, status core.WorkflowStatus) ([]*core.Workflow, error) {
	rows, err := s.q.ListRootWorkflowsByStatus(ctx, int32(status))
	if err != nil {
		return nil, err
	}
	return workflowsFromPG(rows), nil
}

func (s *workflowStore) UpdateWorkflowWorkerStarted(ctx context.Context, workflowID string) error {
	return s.q.UpdateWorkflowWorkerStarted(ctx, workflowID)
}

func (s *workflowStore) UpdateWorkflowWorkerStopped(ctx context.Context, workflowID string) error {
	return s.q.UpdateWorkflowWorkerStopped(ctx, workflowID)
}

func (s *workflowStore) CreateStepExecution(ctx context.Context, exec *core.StepExecution) error {
	_, err := s.q.CreateStepExecution(ctx, pgdb.CreateStepExecutionParams{
		ID:            exec.ID,
		WorkflowID:    exec.WorkflowID,
		StepID:        exec.StepID,
		ActivityName:  exec.ActivityName,
		OutputJson:    exec.OutputJSON,
		ExitCode:      exec.ExitCode,
		Success:       workflowSuccessBoolToNullInt64(exec.Success),
		DurationMs:    exec.DurationMs,
		LoopNodeID:    exec.LoopNodeID,
		LoopIteration: exec.LoopIteration,
		CreatedAt:     exec.CreatedAt,
	})
	return err
}

func (s *workflowStore) GetStepExecution(ctx context.Context, id string) (*core.StepExecution, error) {
	row, err := s.q.GetStepExecution(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("step execution not found: %s", id)
		}
		return nil, err
	}
	return stepExecutionFromPG(row), nil
}

func (s *workflowStore) GetStepExecutionsByWorkflow(ctx context.Context, workflowID string) ([]*core.StepExecution, error) {
	rows, err := s.q.GetAllStepExecutionsForWorkflow(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	items := make([]*core.StepExecution, len(rows))
	for i, row := range rows {
		items[i] = stepExecutionFromPG(row)
	}
	return items, nil
}

func (s *workflowStore) GetStepExecutionsByStep(ctx context.Context, workflowID, stepID string) ([]*core.StepExecution, error) {
	rows, err := s.q.GetStepExecutions(ctx, pgdb.GetStepExecutionsParams{WorkflowID: workflowID, StepID: stepID})
	if err != nil {
		return nil, err
	}
	items := make([]*core.StepExecution, len(rows))
	for i, row := range rows {
		items[i] = stepExecutionFromPG(row)
	}
	return items, nil
}

func (s *workflowStore) DeleteStepExecutionsByWorkflow(ctx context.Context, workflowID string) error {
	return s.q.DeleteStepExecutionsForWorkflow(ctx, workflowID)
}

func (s *workflowStore) ListCommandFavorites(ctx context.Context, userID, projectID string) ([]string, error) {
	return s.q.ListCommandFavorites(ctx, pgdb.ListCommandFavoritesParams{UserID: userID, ProjectID: projectID})
}

func (s *workflowStore) AddCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error {
	return s.q.AddCommandFavorite(ctx, pgdb.AddCommandFavoriteParams{
		ID:         uuid.New().String(),
		UserID:     userID,
		ProjectID:  projectID,
		CommandKey: commandKey,
	})
}

func (s *workflowStore) RemoveCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error {
	return s.q.RemoveCommandFavorite(ctx, pgdb.RemoveCommandFavoriteParams{
		UserID:     userID,
		ProjectID:  projectID,
		CommandKey: commandKey,
	})
}

func workflowFromPG(row pgdb.Workflow) *core.Workflow {
	return &core.Workflow{
		ID:              row.ID,
		ParentID:        nullStringToPtr(row.ParentID),
		ChatID:          row.ChatID,
		WorkflowName:    row.WorkflowName,
		Thread:          row.Thread,
		Status:          core.WorkflowStatus(row.Status),
		SpawnedByNodeID: nullStringToPtr(row.SpawnedByNodeID),
		LoopIteration:   workflowNullInt64ToPtr(row.LoopIteration),
		CreatedAt:       row.CreatedAt,
		CompletedAt:     workflowNullTimeToPtr(row.CompletedAt),
		WorkerStartedAt: workflowNullTimeToPtr(row.WorkerStartedAt),
		WorkerStoppedAt: workflowNullTimeToPtr(row.WorkerStoppedAt),
	}
}

func workflowsFromPG(rows []pgdb.Workflow) []*core.Workflow {
	items := make([]*core.Workflow, len(rows))
	for i, row := range rows {
		items[i] = workflowFromPG(row)
	}
	return items
}

func stepExecutionFromPG(row pgdb.StepExecution) *core.StepExecution {
	var success sql.NullBool
	if row.Success.Valid {
		success = sql.NullBool{Bool: row.Success.Int64 != 0, Valid: true}
	}
	return &core.StepExecution{
		ID:            row.ID,
		WorkflowID:    row.WorkflowID,
		StepID:        row.StepID,
		ActivityName:  row.ActivityName,
		OutputJSON:    row.OutputJson,
		ExitCode:      row.ExitCode,
		Success:       success,
		DurationMs:    row.DurationMs,
		LoopNodeID:    row.LoopNodeID,
		LoopIteration: row.LoopIteration,
		CreatedAt:     row.CreatedAt,
	}
}

func workflowPtrToNullTime(t *time.Time) sql.NullTime {
	if t != nil {
		return sql.NullTime{Time: *t, Valid: true}
	}
	return sql.NullTime{}
}

func workflowNullTimeToPtr(nt sql.NullTime) *time.Time {
	if nt.Valid {
		return &nt.Time
	}
	return nil
}

func workflowPtrToNullInt64(v *int64) sql.NullInt64 {
	if v != nil {
		return sql.NullInt64{Int64: *v, Valid: true}
	}
	return sql.NullInt64{}
}

func workflowNullInt64ToPtr(v sql.NullInt64) *int64 {
	if v.Valid {
		return &v.Int64
	}
	return nil
}

func workflowSuccessBoolToNullInt64(success sql.NullBool) sql.NullInt64 {
	if !success.Valid {
		return sql.NullInt64{}
	}
	if success.Bool {
		return sql.NullInt64{Int64: 1, Valid: true}
	}
	return sql.NullInt64{Int64: 0, Valid: true}
}
