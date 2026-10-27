package store

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// TriggerCapacityExecution is immutable launch identity, not the chat's mutable current run.
type TriggerCapacityExecution struct {
	EventID    string `bun:"id"`
	WorkflowID string `bun:"capacity_workflow_id"`
	FirstRunID string `bun:"capacity_first_run_id"`
}

// BindTriggerCapacityExecution is retry-safe and never replaces a different chain.
func BindTriggerCapacityExecution(ctx context.Context, db orm.Context, eventID, workflowID, firstRunID string) error {
	if eventID == "" || workflowID == "" || firstRunID == "" {
		return errors.New("invalid capacity execution identity")
	}
	res, err := db.Bun().NewRaw(`UPDATE trigger_events SET capacity_workflow_id = ?, capacity_first_run_id = ?
 WHERE id = ? AND capacity_order IS NOT NULL AND outcome = 'launched' AND NOT capacity_released
 AND (capacity_workflow_id IS NULL OR (capacity_workflow_id = ? AND capacity_first_run_id = ?))`, workflowID, firstRunID, eventID, workflowID, firstRunID).Exec(ctx)
	if err != nil {
		return err
	}
	ok, err := oneRowTouched(res)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("capacity execution binding refused")
	}
	return nil
}

// ListTriggerCapacityExecutions includes every occupied slot, even if its activation/chat disappeared.
func ListTriggerCapacityExecutions(ctx context.Context, db orm.Context, scope TriggerCapacityScope) ([]TriggerCapacityExecution, error) {
	var rows []TriggerCapacityExecution
	err := db.Bun().NewRaw(`SELECT id, COALESCE(capacity_workflow_id, '') AS capacity_workflow_id, COALESCE(capacity_first_run_id, '') AS capacity_first_run_id
 FROM trigger_events WHERE user_id = ? AND capacity_project_id = ? AND capacity_workflow_ref = ?
 AND outcome = 'launched' AND NOT capacity_released ORDER BY capacity_order`, scope.UserID, scope.ProjectID, scope.WorkflowRef).Scan(ctx, &rows)
	return rows, err
}

// ReleaseTriggerCapacityExecution accepts only the exact immutable chain observed by the caller.
func ReleaseTriggerCapacityExecution(ctx context.Context, db orm.Context, execution TriggerCapacityExecution) error {
	if execution.EventID == "" || execution.WorkflowID == "" || execution.FirstRunID == "" {
		return errors.New("invalid capacity release identity")
	}
	_, err := db.Bun().NewRaw(`UPDATE trigger_events SET capacity_released = true WHERE id = ? AND capacity_workflow_id = ? AND capacity_first_run_id = ? AND outcome = 'launched'`, execution.EventID, execution.WorkflowID, execution.FirstRunID).Exec(ctx)
	return err
}

// CapacityScopeForEvent reads enrollment for launch retries without inferring it from a mutable chat.
func CapacityScopeForEvent(ctx context.Context, db orm.Context, eventID string) (*TriggerCapacityScope, error) {
	var ev core.TriggerEvent
	err := db.Bun().NewRaw(`SELECT user_id, capacity_project_id, capacity_workflow_ref FROM trigger_events WHERE id = ? AND capacity_order IS NOT NULL`, eventID).Scan(ctx, &ev)
	if err != nil {
		return nil, err
	}
	if ev.CapacityProjectID == nil || ev.CapacityWorkflowRef == nil {
		return nil, errors.New("missing capacity scope")
	}
	return &TriggerCapacityScope{UserID: ev.UserID, ProjectID: *ev.CapacityProjectID, WorkflowRef: *ev.CapacityWorkflowRef}, nil
}
