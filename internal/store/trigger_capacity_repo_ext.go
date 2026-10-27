package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// TriggerCapacityScope identifies a workflow's trigger pool independently of
// mutable activation rows. The caller supplies the resolved workflow ref, never
// its display title. A deleted activation/chat cannot erase occupied capacity.
type TriggerCapacityScope struct {
	UserID      string
	ProjectID   string
	WorkflowRef string
}

func (s TriggerCapacityScope) valid() bool {
	return s.UserID != "" && s.ProjectID != "" && s.WorkflowRef != ""
}

func lockTriggerCapacity(ctx context.Context, db orm.Context, scope TriggerCapacityScope) error {
	if !orm.InTx(ctx) {
		return errors.New("trigger capacity requires the launch transaction")
	}
	// JSON framing prevents ambiguous concatenated scope components. Hash
	// collisions only serialize unrelated pools; correctness never uses the hash
	// for identity. Every query below compares all scope columns explicitly.
	key, err := json.Marshal([]string{scope.UserID, scope.ProjectID, scope.WorkflowRef})
	if err != nil {
		return err
	}
	_, err = db.Bun().NewRaw(`SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))`, "trigger-capacity:"+string(key)).Exec(ctx)
	return err
}

// EnqueueTriggerCapacity durably records a filtered trigger event and assigns
// its immutable FIFO ordinal under the same lock used at launch. It is safe to
// retry after commit/ack loss. It does not claim a slot or create a chat.
func EnqueueTriggerCapacity(ctx context.Context, db orm.Context, ev *core.TriggerEvent, scope TriggerCapacityScope) error {
	if ev == nil || !scope.valid() || ev.UserID != scope.UserID || (ev.Outcome != core.TriggerEventPending && ev.Outcome != core.TriggerEventLaunched) {
		return errors.New("invalid trigger capacity intent")
	}
	return db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if err := lockTriggerCapacity(ctx, db, scope); err != nil {
			return err
		}
		if _, err := InsertTriggerEventIfAbsent(ctx, db, ev); err != nil {
			return err
		}
		var row struct {
			ID          string
			UserID      string
			ProjectID   *string `bun:"capacity_project_id"`
			WorkflowRef *string `bun:"capacity_workflow_ref"`
		}
		err := db.Bun().NewRaw(`SELECT id, user_id, capacity_project_id, capacity_workflow_ref
FROM trigger_events WHERE kind = ? AND dedupe_key = ? FOR UPDATE`, ev.Kind, ev.DedupeKey).Scan(ctx, &row)
		if err != nil {
			return err
		}
		if row.UserID != scope.UserID {
			return errors.New("trigger capacity owner mismatch")
		}
		if row.ProjectID != nil || row.WorkflowRef != nil {
			if row.ProjectID == nil || row.WorkflowRef == nil || *row.ProjectID != scope.ProjectID || *row.WorkflowRef != scope.WorkflowRef {
				return errors.New("trigger capacity scope is immutable")
			}
			return nil
		}
		res, err := db.Bun().NewRaw(`UPDATE trigger_events
SET capacity_project_id = ?, capacity_workflow_ref = ?, capacity_order = nextval('trigger_capacity_order_seq')
WHERE id = ? AND outcome = 'pending' AND capacity_order IS NULL`, scope.ProjectID, scope.WorkflowRef, row.ID).Exec(ctx)
		if err != nil {
			return err
		}
		ok, err := oneRowTouched(res)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cannot enroll an already settled trigger event")
		}
		return nil
	})
}

// CanLaunchTriggerCapacity checks FIFO and capacity inside the caller's launch
// transaction. The caller MUST claim the pending event in that same transaction
// before releasing the lock (ClaimPendingTriggerEvent already does this).
// Missing terminal evidence retains a slot, including half-starts, paused runs,
// deleted chats and unknown statuses. Only the Temporal-confirmed release flag
// frees capacity; best-effort chat/run status never releases a slot.
func CanLaunchTriggerCapacity(ctx context.Context, db orm.Context, eventID string, scope TriggerCapacityScope, maxRunning uint32) (bool, error) {
	if !scope.valid() || eventID == "" || maxRunning == 0 {
		return false, errors.New("invalid trigger capacity policy")
	}
	if err := lockTriggerCapacity(ctx, db, scope); err != nil {
		return false, err
	}
	var allowed bool
	err := db.Bun().NewRaw(`SELECT EXISTS (
 SELECT 1 FROM trigger_events candidate
 WHERE candidate.id = ?0 AND candidate.user_id = ?1
   AND candidate.capacity_project_id = ?2 AND candidate.capacity_workflow_ref = ?3
   AND candidate.capacity_order IS NOT NULL AND candidate.outcome = 'pending'
   AND NOT EXISTS (
     SELECT 1 FROM trigger_events older
     WHERE older.user_id = ?1 AND older.capacity_project_id = ?2 AND older.capacity_workflow_ref = ?3
       AND older.outcome = 'pending' AND older.capacity_order < candidate.capacity_order
   )
   AND (SELECT count(*) FROM trigger_events occupied
     WHERE occupied.user_id = ?1 AND occupied.capacity_project_id = ?2 AND occupied.capacity_workflow_ref = ?3
       AND occupied.capacity_order IS NOT NULL AND occupied.outcome = 'launched'
       AND NOT occupied.capacity_released
   ) < ?4
)`, eventID, scope.UserID, scope.ProjectID, scope.WorkflowRef, maxRunning).Scan(ctx, &allowed)
	if err != nil {
		return false, fmt.Errorf("check trigger capacity: %w", err)
	}
	return allowed, nil
}
