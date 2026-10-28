package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ReleaseUnboundTriggerCapacity is permitted only after the caller has obtained
// fresh external evidence that no admitted execution can still be running. The
// root is a launch fence, not terminal-execution evidence: never infer release
// from its status alone.
func ReleaseUnboundTriggerCapacity(ctx context.Context, db orm.Context, e TriggerCapacityExecution, reason string) (bool, error) {
	if e.EventID == "" || e.WorkflowID == "" || e.FirstRunID != "" || reason == "" {
		return false, errors.New("invalid unbound capacity recovery")
	}
	res, err := db.Bun().NewRaw(`UPDATE trigger_events event SET capacity_released=true,capacity_released_at=now(),capacity_release_reason=?
 WHERE event.id=? AND event.capacity_workflow_id=? AND event.capacity_first_run_id IS NULL
 AND event.outcome='launched' AND NOT event.capacity_released
 AND NOT EXISTS(SELECT 1 FROM workflows root WHERE root.id=event.capacity_workflow_id AND root.state=1)`, reason, e.EventID, e.WorkflowID).Exec(ctx)
	if err != nil {
		return false, err
	}
	return oneRowTouched(res)
}

// FenceUnboundTriggerCapacityStart abandons only a known failed pending start.
// A racing retry clears start_failed_at under the same row lock and wins safely.
func FenceUnboundTriggerCapacityStart(ctx context.Context, db orm.Context, e TriggerCapacityExecution) (bool, error) {
	if e.EventID == "" || e.WorkflowID == "" || e.FirstRunID != "" {
		return false, errors.New("invalid unbound capacity fence")
	}
	res, err := db.Bun().NewRaw(`UPDATE workflows root SET state=3,stop_reason=2
 WHERE root.id=? AND root.state=1 AND root.start_failed_at < now()-interval '5 minutes'
 AND EXISTS(SELECT 1 FROM trigger_events event WHERE event.id=? AND event.capacity_workflow_id=root.id AND event.capacity_first_run_id IS NULL AND event.outcome='launched' AND NOT event.capacity_released)`, e.WorkflowID, e.EventID).Exec(ctx)
	if err != nil {
		return false, err
	}
	return oneRowTouched(res)
}

// TriggerCapacityStartFenced reports a durable prohibition on future launch
// retries. It is never enough by itself to release an accepted external run.
func TriggerCapacityStartFenced(ctx context.Context, db orm.Context, e TriggerCapacityExecution) (bool, error) {
	var fenced bool
	err := db.Bun().NewRaw(`SELECT NOT EXISTS(SELECT 1 FROM workflows WHERE id=? AND state=1)`, e.WorkflowID).Scan(ctx, &fenced)
	return fenced, err
}

// ReleaseTriggerCapacityByOperator requires the operator to fence/terminate the
// external execution FIRST and records that decision. There is no automatic TTL.
func ReleaseTriggerCapacityByOperator(ctx context.Context, db orm.Context, eventID, actor, note string) (bool, error) {
	if eventID == "" || strings.TrimSpace(actor) == "" || strings.TrimSpace(note) == "" {
		return false, errors.New("operator identity and fencing evidence are required")
	}
	res, err := db.Bun().NewRaw(`UPDATE trigger_events event SET capacity_released=true,capacity_released_at=now(),capacity_release_reason=?
 WHERE event.id=? AND event.capacity_order IS NOT NULL AND event.outcome='launched' AND NOT event.capacity_released
 AND event.capacity_workflow_id IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM workflows root WHERE root.id=event.capacity_workflow_id AND root.state=1)`, fmt.Sprintf("operator:%s:%s", actor, note), eventID).Exec(ctx)
	if err != nil {
		return false, err
	}
	changed, err := oneRowTouched(res)
	if err == nil && changed {
		logging.WarnContext(ctx, "Workflow capacity released by operator", "event_id", eventID, "actor", actor)
	}
	return changed, err
}
