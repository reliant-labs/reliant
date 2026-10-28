package store

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// TriggerCapacityExecution is immutable launch identity, not the chat's mutable current run.
type TriggerCapacityExecution struct {
	EventID          string     `bun:"id"`
	WorkflowID       string     `bun:"capacity_workflow_id"`
	FirstRunID       string     `bun:"capacity_first_run_id"`
	BoundAt          *time.Time `bun:"capacity_bound_at"`
	StartAttemptAt   *time.Time `bun:"capacity_start_attempt_at"`
	StartAttemptAged bool       `bun:"capacity_start_attempt_aged"`
}

// PrepareTriggerCapacityExecution records identity before the external start,
// and stamps the launch's start attempt (see ReleaseUnboundTriggerCapacity).
func PrepareTriggerCapacityExecution(ctx context.Context, db orm.Context, eventID, workflowID string) error {
	if !orm.InTx(ctx) || eventID == "" || workflowID == "" {
		return errors.New("capacity launch identity requires launch transaction")
	}
	res, err := db.Bun().NewRaw(`UPDATE trigger_events SET capacity_workflow_id=?, capacity_start_attempt_at=clock_timestamp()
 WHERE id=? AND capacity_order IS NOT NULL AND outcome='launched' AND NOT capacity_released
 AND (capacity_workflow_id IS NULL OR capacity_workflow_id=?)`, workflowID, eventID, workflowID).Exec(ctx)
	if err != nil {
		return err
	}
	ok, err := oneRowTouched(res)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("capacity launch identity refused")
	}
	return nil
}

// BindTriggerCapacityExecution is retry-safe and never replaces a different chain.
func BindTriggerCapacityExecution(ctx context.Context, db orm.Context, eventID, workflowID, firstRunID string) error {
	if eventID == "" || workflowID == "" || firstRunID == "" {
		return errors.New("invalid capacity execution identity")
	}
	res, err := db.Bun().NewRaw(`UPDATE trigger_events SET capacity_workflow_id = ?, capacity_first_run_id = ?, capacity_bound_at = COALESCE(capacity_bound_at,now()),
 capacity_start_attempt_at = COALESCE(capacity_start_attempt_at,clock_timestamp())
 WHERE id = ? AND capacity_order IS NOT NULL AND outcome = 'launched' AND NOT capacity_released
 AND (capacity_workflow_id IS NULL OR capacity_workflow_id = ?) AND (capacity_first_run_id IS NULL OR capacity_first_run_id = ?)`, workflowID, firstRunID, eventID, workflowID, firstRunID).Exec(ctx)
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
	err := db.Bun().NewRaw(`SELECT id, COALESCE(capacity_workflow_id, '') AS capacity_workflow_id, COALESCE(capacity_first_run_id, '') AS capacity_first_run_id, capacity_bound_at, capacity_start_attempt_at,
 COALESCE(capacity_start_attempt_at < now()-make_interval(secs => ?),false) AS capacity_start_attempt_aged
 FROM trigger_events WHERE user_id = ? AND capacity_project_id = ? AND capacity_workflow_ref = ?
 AND outcome = 'launched' AND NOT capacity_released ORDER BY capacity_order`, startAttemptBoundSeconds(), scope.UserID, scope.ProjectID, scope.WorkflowRef).Scan(ctx, &rows)
	return rows, err
}

// ListTriggerCapacityExecutionBatch rotates every occupied slot, even with no
// queued successor, so terminal evidence is persisted before Temporal retention.
func ListTriggerCapacityExecutionBatch(ctx context.Context, db orm.Context, limit int) ([]TriggerCapacityExecution, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []TriggerCapacityExecution
	err := db.Bun().NewRaw(`WITH selected AS (
 SELECT id FROM trigger_events WHERE capacity_order IS NOT NULL AND outcome='launched' AND NOT capacity_released
 ORDER BY capacity_checked_at NULLS FIRST,capacity_order LIMIT ? FOR UPDATE SKIP LOCKED
 ), touched AS (
 UPDATE trigger_events SET capacity_checked_at=now() WHERE id IN (SELECT id FROM selected)
 RETURNING id,COALESCE(capacity_workflow_id,'') AS capacity_workflow_id,COALESCE(capacity_first_run_id,'') AS capacity_first_run_id,capacity_bound_at,capacity_start_attempt_at,
 COALESCE(capacity_start_attempt_at < now()-make_interval(secs => ?),false) AS capacity_start_attempt_aged
 ) SELECT * FROM touched`, limit, startAttemptBoundSeconds()).Scan(ctx, &rows)
	return rows, err
}

// ReleaseTriggerCapacityExecution accepts only the exact immutable chain observed by the caller.
func ReleaseTriggerCapacityExecution(ctx context.Context, db orm.Context, execution TriggerCapacityExecution) error {
	if execution.EventID == "" || execution.WorkflowID == "" || execution.FirstRunID == "" {
		return errors.New("invalid capacity release identity")
	}
	if !execution.StartAttemptAged || execution.StartAttemptAt == nil {
		return nil
	}
	_, err := db.Bun().NewRaw(`UPDATE trigger_events event SET capacity_released = true, capacity_released_at=now(), capacity_release_reason='confirmed_closed_or_superseded' WHERE id = ? AND capacity_workflow_id = ? AND capacity_first_run_id = ? AND outcome = 'launched' AND NOT capacity_released AND `+startAttemptAged+` AND capacity_start_attempt_at = ?`, execution.EventID, execution.WorkflowID, execution.FirstRunID, startAttemptBoundSeconds(), execution.StartAttemptAt).Exec(ctx)
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
