package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// startAttemptAged is the database-side fence on a start still in flight: the
// event's last start attempt (stamped by the launch transaction and by every
// retry's claim, under the event's row lock) is older than
// core.StartAttemptBound. A root that left PENDING is not that fence on its
// own: runs.Service.Terminate and chat deletion move or remove a root without
// waiting for a start that already passed its claim.
const startAttemptAged = `event.capacity_start_attempt_at < now() - make_interval(secs => ?)`

func startAttemptBoundSeconds() float64 { return core.StartAttemptBound.Seconds() }

// ReleaseUnboundTriggerCapacity is permitted only after the caller has obtained
// fresh external evidence that no admitted execution can still be running. The
// root is a launch fence, not terminal-execution evidence: never infer release
// from its status alone. The UPDATE itself also requires the last start
// attempt to be older than the start-attempt bound, so a start that is still
// in flight (whose RPC is bounded by core.StartAttemptDeadline) holds the slot
// however its root left PENDING, and a retry that claims the start first
// refreshes the stamp and wins.
func ReleaseUnboundTriggerCapacity(ctx context.Context, db orm.Context, e TriggerCapacityExecution, reason string) (bool, error) {
	if e.EventID == "" || e.WorkflowID == "" || e.FirstRunID != "" || reason == "" {
		return false, errors.New("invalid unbound capacity recovery")
	}
	if !e.StartAttemptAged || e.StartAttemptAt == nil {
		return false, nil
	}
	res, err := db.Bun().NewRaw(`UPDATE trigger_events event SET capacity_released=true,capacity_released_at=now(),capacity_release_reason=?
 WHERE event.id=? AND event.capacity_workflow_id=? AND event.capacity_first_run_id IS NULL
 AND event.outcome='launched' AND NOT event.capacity_released AND `+startAttemptAged+` AND capacity_start_attempt_at = ?
 AND NOT EXISTS(SELECT 1 FROM workflows root WHERE root.id=event.capacity_workflow_id AND root.state=1)`, reason, e.EventID, e.WorkflowID, startAttemptBoundSeconds(), e.StartAttemptAt).Exec(ctx)
	if err != nil {
		return false, err
	}
	return oneRowTouched(res)
}

// FenceUnboundTriggerCapacityStart abandons only a known failed pending start,
// the AbandonFailedWorkflowStart shape with the start-attempt bound added. A
// racing retry clears start_failed_at under the same row lock and wins safely.
func FenceUnboundTriggerCapacityStart(ctx context.Context, db orm.Context, e TriggerCapacityExecution) (bool, error) {
	if e.EventID == "" || e.WorkflowID == "" || e.FirstRunID != "" {
		return false, errors.New("invalid unbound capacity fence")
	}
	res, err := setWorkflowStatus(db.Bun().NewUpdate().Model((*Workflow)(nil)), core.Failed()).
		Where("id = ?", e.WorkflowID).
		Where("state = ?", core.WorkflowStatePending).
		Where("start_failed_at < now() - make_interval(secs => ?)", startAttemptBoundSeconds()).
		Where(`EXISTS(SELECT 1 FROM trigger_events event WHERE event.id=? AND event.capacity_workflow_id=?
 AND event.capacity_first_run_id IS NULL AND event.outcome='launched' AND NOT event.capacity_released AND `+startAttemptAged+`)`,
			e.EventID, e.WorkflowID, startAttemptBoundSeconds()).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	return oneRowTouched(res)
}

// TriggerCapacityStartFenced reports a durable prohibition on future launch
// retries. It is never enough by itself to release an accepted external run.
func TriggerCapacityStartFenced(ctx context.Context, db orm.Context, e TriggerCapacityExecution) (bool, error) {
	var fenced bool
	err := db.Bun().NewRaw(`SELECT EXISTS(SELECT 1 FROM trigger_events event WHERE event.id=? AND event.capacity_workflow_id=? AND NOT event.capacity_released AND event.capacity_start_attempt_at=? AND `+startAttemptAged+`) AND NOT EXISTS(SELECT 1 FROM workflows WHERE id=? AND state=1)`, e.EventID, e.WorkflowID, e.StartAttemptAt, startAttemptBoundSeconds(), e.WorkflowID).Scan(ctx, &fenced)
	return fenced, err
}

// ReleaseTriggerCapacityByOperator requires the operator to fence/terminate the
// external execution FIRST and records that decision. There is no automatic TTL.
// The UPDATE enforces what it can: a non-PENDING (or missing) root, and a last
// start attempt older than the start-attempt bound, so an operator cannot
// release a slot whose start may still be accepted. A bound chain's
// termination in Temporal is the operator's prerequisite; it cannot be
// checked here.
func ReleaseTriggerCapacityByOperator(ctx context.Context, db orm.Context, observed TriggerCapacityExecution, actor, note string) (bool, error) {
	eventID := observed.EventID
	if eventID == "" || strings.TrimSpace(actor) == "" || strings.TrimSpace(note) == "" {
		return false, errors.New("operator identity and fencing evidence are required")
	}
	if !observed.StartAttemptAged || observed.StartAttemptAt == nil {
		return false, nil
	}
	res, err := db.Bun().NewRaw(`UPDATE trigger_events event SET capacity_released=true,capacity_released_at=now(),capacity_release_reason=?
 WHERE event.id=? AND event.capacity_order IS NOT NULL AND event.outcome='launched' AND NOT event.capacity_released
 AND event.capacity_workflow_id IS NOT NULL AND `+startAttemptAged+` AND capacity_start_attempt_at=?
 AND NOT EXISTS(SELECT 1 FROM workflows root WHERE root.id=event.capacity_workflow_id AND root.state=1)`, fmt.Sprintf("operator:%s:%s", actor, note), eventID, startAttemptBoundSeconds(), observed.StartAttemptAt).Exec(ctx)
	if err != nil {
		return false, err
	}
	changed, err := oneRowTouched(res)
	if err == nil && changed {
		logging.WarnContext(ctx, "Workflow capacity released by operator", "event_id", eventID, "actor", actor)
	}
	return changed, err
}
