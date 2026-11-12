package store

import (
	"context"
	"database/sql"
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

// TriggerCapacitySuperseded is the outcome_detail of a waiting event that a
// newer event of the same concurrency key replaced (`waiting: latest`). The
// event is settled skipped: superseding is the policy working, not a failure.
const TriggerCapacitySuperseded = "superseded by a newer firing (concurrency waiting: latest)"

// EnqueueTriggerCapacity durably records a filtered trigger event and assigns
// its immutable FIFO ordinal under the same lock used at launch. It is safe to
// retry after commit/ack loss. It does not claim a slot or create a chat.
//
// An event with a CapacityKey also supersedes, in the same transaction, every
// older WAITING event of its scope and key: they settle skipped with
// TriggerCapacitySuperseded. Launched events are never touched, and a retried
// enrollment supersedes nothing more.
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
		var order int64
		err = db.Bun().NewRaw(`UPDATE trigger_events
SET capacity_project_id = ?, capacity_workflow_ref = ?, capacity_key = ?, capacity_order = nextval('trigger_capacity_order_seq')
WHERE id = ? AND outcome = 'pending' AND capacity_order IS NULL
RETURNING capacity_order`, scope.ProjectID, scope.WorkflowRef, ev.CapacityKey, row.ID).Scan(ctx, &order)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("cannot enroll an already settled trigger event")
		}
		if err != nil {
			return err
		}
		if ev.CapacityKey == nil {
			return nil
		}
		_, err = db.Bun().NewRaw(`UPDATE trigger_events
SET outcome = 'skipped', outcome_detail = ?
WHERE user_id = ? AND capacity_project_id = ? AND capacity_workflow_ref = ? AND capacity_key = ?
  AND outcome = 'pending' AND capacity_order < ?`,
			TriggerCapacitySuperseded, scope.UserID, scope.ProjectID, scope.WorkflowRef, *ev.CapacityKey, order).Exec(ctx)
		if err != nil {
			return fmt.Errorf("supersede waiting trigger events: %w", err)
		}
		return nil
	})
}

// NextWaitingTriggerCapacityEvent returns the head of a scope's FIFO queue —
// its oldest waiting event, the only one CanLaunchTriggerCapacity may admit —
// or nil when nothing waits. Served by trigger_events_capacity_waiting.
func NextWaitingTriggerCapacityEvent(ctx context.Context, db orm.Context, scope TriggerCapacityScope) (*core.TriggerEvent, error) {
	if !scope.valid() {
		return nil, errors.New("invalid trigger capacity scope")
	}
	var rows []*TriggerEvent
	err := db.Bun().NewRaw(`SELECT `+columnsOf[TriggerEvent]("event")+` FROM trigger_events event
WHERE event.user_id = ? AND event.capacity_project_id = ? AND event.capacity_workflow_ref = ?
  AND event.capacity_order IS NOT NULL AND event.outcome = 'pending'
ORDER BY event.capacity_order LIMIT 1`, scope.UserID, scope.ProjectID, scope.WorkflowRef).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("find waiting trigger capacity head: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return triggerEventToCore(rows[0])
}

// occupiedForAdmission is the slot predicate queued admission counts, over an
// alias named occupied: a launched run that was not released, unless it is
// parked on a provider (ParkTriggerCapacity) and has not asked for its slot
// back. A parked run that asked is ahead of every queued firing, so it counts.
const occupiedForAdmission = `occupied.capacity_order IS NOT NULL AND occupied.outcome = 'launched'
       AND NOT occupied.capacity_released
       AND (occupied.capacity_parked_at IS NULL OR occupied.capacity_resume_requested_at IS NOT NULL)`

// CanLaunchTriggerCapacity checks FIFO and capacity inside the caller's launch
// transaction. The caller MUST claim the pending event in that same transaction
// before releasing the lock (ClaimPendingTriggerEvent already does this).
// Missing terminal evidence retains a slot, including half-starts, paused runs,
// deleted chats and unknown statuses. Only the Temporal-confirmed release flag
// frees capacity; best-effort chat/run status never releases a slot. The one
// exception is a run parked on a provider, which holds none while it waits
// (ParkTriggerCapacity).
//
// An admitted event is stamped with maxRunning: the cap a parked run resumes
// against (ResumeTriggerCapacity).
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
       AND `+occupiedForAdmission+`
   ) < ?4
)`, eventID, scope.UserID, scope.ProjectID, scope.WorkflowRef, maxRunning).Scan(ctx, &allowed)
	if err != nil {
		return false, fmt.Errorf("check trigger capacity: %w", err)
	}
	if allowed {
		if _, err := db.Bun().NewRaw(`UPDATE trigger_events SET capacity_limit = ? WHERE id = ?`, maxRunning, eventID).Exec(ctx); err != nil {
			return false, fmt.Errorf("stamp trigger capacity limit: %w", err)
		}
	}
	return allowed, nil
}

// TriggerCapacitySlot names the slot one run holds: its trigger event and the
// exact chain admitted for it. A later chain on the same chat (a human turn)
// never matches.
type TriggerCapacitySlot struct {
	EventID    string
	WorkflowID string
	FirstRunID string
}

func (s TriggerCapacitySlot) valid() bool {
	return s.EventID != "" && s.WorkflowID != "" && s.FirstRunID != ""
}

// heldSlotSQL matches the row of a slot the run still holds or has parked, by
// its event and chain (?0 event, ?1 workflow id, ?2 first run id). A chain not
// yet bound matches on its workflow id; the binding follows.
const heldSlotSQL = `id = ?0 AND capacity_order IS NOT NULL AND outcome = 'launched' AND NOT capacity_released
 AND capacity_workflow_id = ?1 AND (capacity_first_run_id IS NULL OR capacity_first_run_id = ?2)`

// ParkTriggerCapacity gives back the slot of a run that is waiting on a
// provider: until it resumes (ResumeTriggerCapacity) it counts toward neither
// queued admission nor resumes. held is false when the run holds no slot to
// park (it was not capacity-admitted, the slot was released, or the chain is a
// later one); the caller then stops asking. Idempotent.
func ParkTriggerCapacity(ctx context.Context, db orm.Context, slot TriggerCapacitySlot) (held bool, err error) {
	if !slot.valid() {
		return false, errors.New("invalid trigger capacity slot")
	}
	res, err := db.Bun().NewRaw(`UPDATE trigger_events SET capacity_parked_at = COALESCE(capacity_parked_at, now()), capacity_resume_requested_at = NULL
 WHERE `+heldSlotSQL, slot.EventID, slot.WorkflowID, slot.FirstRunID).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("park trigger capacity: %w", err)
	}
	return oneRowTouched(res)
}

// ResumeTriggerCapacity takes back the slot of a parked run, under the same
// scope lock admission takes. It is granted while fewer than the event's
// admitted cap of the scope's runs are running (not parked); otherwise the run
// is marked as asking, which puts it ahead of every queued firing
// (occupiedForAdmission) without preempting anything, and it asks again later.
//
// held is false when the run holds no slot (see ParkTriggerCapacity); granted
// is then true, as it is for a run that was not parked. A row admitted before
// caps were stamped resumes unconditionally.
func ResumeTriggerCapacity(ctx context.Context, db orm.Context, slot TriggerCapacitySlot) (held, granted bool, err error) {
	if !slot.valid() {
		return false, false, errors.New("invalid trigger capacity slot")
	}
	err = db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var rows []struct {
			UserID      string  `bun:"user_id"`
			ProjectID   string  `bun:"capacity_project_id"`
			WorkflowRef string  `bun:"capacity_workflow_ref"`
			Parked      bool    `bun:"parked"`
			Limit       *uint32 `bun:"capacity_limit"`
		}
		read := func() error {
			rows = nil
			return db.Bun().NewRaw(`SELECT user_id, capacity_project_id, capacity_workflow_ref, capacity_parked_at IS NOT NULL AS parked, capacity_limit
 FROM trigger_events WHERE `+heldSlotSQL, slot.EventID, slot.WorkflowID, slot.FirstRunID).Scan(ctx, &rows)
		}
		if err := read(); err != nil {
			return err
		}
		if len(rows) == 0 {
			held, granted = false, true
			return nil
		}
		if !rows[0].Parked {
			held, granted = true, true
			return nil
		}
		if err := lockTriggerCapacity(ctx, db, TriggerCapacityScope{UserID: rows[0].UserID, ProjectID: rows[0].ProjectID, WorkflowRef: rows[0].WorkflowRef}); err != nil {
			return err
		}
		// Re-read under the lock: a release or another resume may have landed.
		if err := read(); err != nil {
			return err
		}
		if len(rows) == 0 {
			held, granted = false, true
			return nil
		}
		row := rows[0]
		held = true
		if !row.Parked {
			granted = true
			return nil
		}
		granted = row.Limit == nil
		if !granted {
			var running uint32
			if err := db.Bun().NewRaw(`SELECT count(*) FROM trigger_events occupied
 WHERE occupied.user_id = ? AND occupied.capacity_project_id = ? AND occupied.capacity_workflow_ref = ?
   AND occupied.capacity_order IS NOT NULL AND occupied.outcome = 'launched' AND NOT occupied.capacity_released
   AND occupied.capacity_parked_at IS NULL`, row.UserID, row.ProjectID, row.WorkflowRef).Scan(ctx, &running); err != nil {
				return err
			}
			granted = running < *row.Limit
		}
		if granted {
			_, err := db.Bun().NewRaw(`UPDATE trigger_events SET capacity_parked_at = NULL, capacity_resume_requested_at = NULL WHERE id = ?`, slot.EventID).Exec(ctx)
			return err
		}
		_, err := db.Bun().NewRaw(`UPDATE trigger_events SET capacity_resume_requested_at = COALESCE(capacity_resume_requested_at, now()) WHERE id = ?`, slot.EventID).Exec(ctx)
		return err
	})
	if err != nil {
		return false, false, fmt.Errorf("resume trigger capacity: %w", err)
	}
	return held, granted, nil
}
