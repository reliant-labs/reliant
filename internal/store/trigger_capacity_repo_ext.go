package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

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

// ErrTriggerCapacityScopeMoved refuses enrolling a firing in a capacity scope
// whose project is no longer its trigger's: the trigger moved (MergeProjects)
// after the fire read it. Nothing is written; the fire fails and is retried,
// and the retry reads the trigger, and so the scope, again.
var ErrTriggerCapacityScopeMoved = errors.New("the trigger moved to another project since its firing read it; the fire is retried")

// fenceTriggerCapacityScope holds the event's trigger FOR SHARE for the rest of
// the enrollment and refuses a scope that is not the trigger's project now
// (ErrTriggerCapacityScopeMoved). The share lock conflicts with the trigger
// lock MergeProjects takes before it counts the source's queued firings, so an
// enrollment either commits before that count (which then sees it and refuses
// the merge) or waits for the merge and finds the trigger moved; a firing is
// never admitted to a bucket its trigger has left. Taken before the capacity
// advisory lock: row lock, then advisory, the order a launch takes them in.
// An event with no trigger (ad hoc kinds), or whose trigger is gone, is not
// fenced.
func fenceTriggerCapacityScope(ctx context.Context, db orm.Context, ev *core.TriggerEvent, scope TriggerCapacityScope) error {
	if ev.TriggerID == nil || *ev.TriggerID == "" {
		return nil
	}
	var project []*string
	if err := db.Bun().NewRaw(`SELECT project_id FROM triggers WHERE id = ? FOR SHARE`, *ev.TriggerID).
		Scan(ctx, &project); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lock the firing's trigger: %w", err)
	}
	if len(project) == 1 && (project[0] == nil || *project[0] != scope.ProjectID) {
		return ErrTriggerCapacityScopeMoved
	}
	return nil
}

// TriggerCapacitySuperseded is the outcome_detail of a waiting event that a
// newer event of the same concurrency key replaced (`waiting: latest`). The
// event is settled skipped: superseding is the policy working, not a failure.
const TriggerCapacitySuperseded = "superseded by a newer firing (concurrency waiting: latest)"

// EnqueueTriggerCapacity durably records a filtered trigger event and assigns
// its immutable arrival ordinal, with its CapacityRunningKey and
// CapacityPriority, under the same lock used at launch. It is safe to retry
// after commit/ack loss. It does not claim a slot or create a chat.
//
// An event with a CapacityKey also supersedes, in the same transaction, every
// older WAITING event of its scope and key: they settle skipped with
// TriggerCapacitySuperseded. Launched events are never touched, and a retried
// enrollment supersedes nothing more.
func EnqueueTriggerCapacity(ctx context.Context, db orm.Context, ev *core.TriggerEvent, scope TriggerCapacityScope) error {
	_, err := EnqueueTriggerCapacitySuperseding(ctx, db, ev, scope)
	return err
}

// EnqueueTriggerCapacitySuperseding is EnqueueTriggerCapacity, returning the
// waiting events the enrollment superseded, as it left them (settled
// skipped), so each can be accounted for: the firer opens a
// trigger.fire span per event. Empty when it superseded none.
func EnqueueTriggerCapacitySuperseding(ctx context.Context, db orm.Context, ev *core.TriggerEvent, scope TriggerCapacityScope) ([]*core.TriggerEvent, error) {
	var superseded []*core.TriggerEvent
	if ev == nil || !scope.valid() || ev.UserID != scope.UserID || (ev.Outcome != core.TriggerEventPending && ev.Outcome != core.TriggerEventLaunched) {
		return nil, errors.New("invalid trigger capacity intent")
	}
	err := db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		superseded = nil // a retried transaction starts over
		if err := fenceTriggerCapacityScope(ctx, db, ev, scope); err != nil {
			return err
		}
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
SET capacity_project_id = ?, capacity_workflow_ref = ?, capacity_key = ?, capacity_running_key = ?, capacity_priority = ?,
  capacity_order = nextval('trigger_capacity_order_seq')
WHERE id = ? AND outcome = 'pending' AND capacity_order IS NULL
RETURNING capacity_order`, scope.ProjectID, scope.WorkflowRef, ev.CapacityKey, ev.CapacityRunningKey, ev.CapacityPriority, row.ID).Scan(ctx, &order)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("cannot enroll an already settled trigger event")
		}
		if err != nil {
			return err
		}
		if ev.CapacityKey == nil {
			return nil
		}
		var rows []*TriggerEvent
		err = db.Bun().NewRaw(`UPDATE trigger_events
SET outcome = 'skipped', outcome_detail = ?
WHERE user_id = ? AND capacity_project_id = ? AND capacity_workflow_ref = ? AND capacity_key = ?
  AND outcome = 'pending' AND capacity_order < ?
RETURNING `+columnsOf[TriggerEvent]("trigger_events"),
			TriggerCapacitySuperseded, scope.UserID, scope.ProjectID, scope.WorkflowRef, *ev.CapacityKey, order).Scan(ctx, &rows)
		if err != nil {
			return fmt.Errorf("supersede waiting trigger events: %w", err)
		}
		for _, row := range rows {
			settled, err := triggerEventToCore(row)
			if err != nil {
				return err
			}
			superseded = append(superseded, settled)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return superseded, nil
}

// TriggerCapacityLimits is a workflow's admission policy as one launch sees
// it: the caps resolved for the firing's trigger now. They are not stored on
// the waiting events, so a changed policy applies to the events already
// waiting.
type TriggerCapacityLimits struct {
	// MaxRunning bounds the scope's running runs. Required (> 0); a policy
	// with only a per-key cap passes TriggerCapacityUnbounded.
	MaxRunning uint32
	// MaxRunningPerKey bounds the launched runs of one capacity_running_key.
	// 0 is no per-key cap. An event with no running key is never held by it.
	MaxRunningPerKey uint32
	// ExpiredBefore is the expiry cutoff: a waiting event recorded before it
	// is never admitted, and the scope admits the next one past it. The zero
	// time expires nothing. The redriver settles expired events
	// (ExpirePendingTriggerCapacityEvents); admission only refuses them, so an
	// event the redriver has not reached yet can never launch late.
	ExpiredBefore time.Time
}

// expiredBefore is ExpiredBefore as the admission query binds it: NULL for
// none.
func (l TriggerCapacityLimits) expiredBefore() any {
	if l.ExpiredBefore.IsZero() {
		return nil
	}
	return l.ExpiredBefore.UTC()
}

// TriggerCapacityUnbounded is the MaxRunning of a policy that caps only per
// key. It fits capacity_limit, the integer a parked run resumes against.
const TriggerCapacityUnbounded = math.MaxInt32

func (l TriggerCapacityLimits) valid() bool {
	return l.MaxRunning > 0 && l.MaxRunning <= TriggerCapacityUnbounded
}

// occupiedForAdmission is the slot predicate queued admission counts, over an
// alias named occupied: a launched run that was not released, unless it is
// parked on a provider (ParkTriggerCapacity) and has not asked for its slot
// back. A parked run that asked is ahead of every queued firing, so it counts.
const occupiedForAdmission = `occupied.capacity_order IS NOT NULL AND occupied.outcome = 'launched'
       AND NOT occupied.capacity_released
       AND (occupied.capacity_parked_at IS NULL OR occupied.capacity_resume_requested_at IS NOT NULL)`

// occupiedForKey is the per-key cap's predicate, over the alias occupied: a
// launched run of the key that was not released. A run parked on a provider
// gives back its workflow-wide slot but keeps its key's: the cap is about the
// subject (one run per pull request), and the parked run still owns it and
// resumes without asking the key.
const occupiedForKey = `occupied.capacity_order IS NOT NULL AND occupied.outcome = 'launched'
       AND NOT occupied.capacity_released`

// nextAdmissibleTriggerCapacitySQL selects the waiting event a scope admits
// next, or no row: the first waiting event in admission order (highest
// capacity_priority, NULL being 0, then oldest capacity_order) that its key's
// running cap does not hold back, provided the scope has a free slot at all.
//
// An event whose key is at its cap is passed over rather than blocking the
// queue: one busy pull request must not stop every other one behind it. It
// keeps its place, and is admitted once its key frees a slot. With no priority
// and no per-key cap this is exactly the FIFO head, when a slot is free.
//
// An expired event (recorded before ExpiredBefore) is passed over too, and
// never admitted: it is the redriver's to settle.
//
// Parameters: ?0 user, ?1 project, ?2 workflow ref, ?3 MaxRunning,
// ?4 MaxRunningPerKey, ?5 ExpiredBefore (NULL for none). The waiting scan is
// trigger_events_capacity_waiting; the occupied counts read
// trigger_events_capacity_occupied, a handful of rows.
const nextAdmissibleTriggerCapacitySQL = ` FROM trigger_events event
WHERE event.user_id = ?0 AND event.capacity_project_id = ?1 AND event.capacity_workflow_ref = ?2
  AND event.capacity_order IS NOT NULL AND event.outcome = 'pending'
  AND (?5::timestamptz IS NULL OR event.created_at >= ?5::timestamptz)
  AND (SELECT count(*) FROM trigger_events occupied
    WHERE occupied.user_id = ?0 AND occupied.capacity_project_id = ?1 AND occupied.capacity_workflow_ref = ?2
      AND ` + occupiedForAdmission + `
  ) < ?3
  AND (?4 = 0 OR event.capacity_running_key IS NULL OR (SELECT count(*) FROM trigger_events occupied
    WHERE occupied.user_id = ?0 AND occupied.capacity_project_id = ?1 AND occupied.capacity_workflow_ref = ?2
      AND occupied.capacity_running_key = event.capacity_running_key
      AND ` + occupiedForKey + `
  ) < ?4)
ORDER BY COALESCE(event.capacity_priority, 0) DESC, event.capacity_order
LIMIT 1`

// NextAdmissibleTriggerCapacityEvent returns the waiting event the scope would
// admit now (the only one CanLaunchTriggerCapacity may admit), or nil when
// nothing waits, every waiting event is held by its key's cap, or the scope
// is full. Outside the launch lock it is a hint: the event's own fire decides,
// under the lock.
func NextAdmissibleTriggerCapacityEvent(ctx context.Context, db orm.Context, scope TriggerCapacityScope, limits TriggerCapacityLimits) (*core.TriggerEvent, error) {
	if !scope.valid() || !limits.valid() {
		return nil, errors.New("invalid trigger capacity policy")
	}
	var rows []*TriggerEvent
	err := db.Bun().NewRaw(`SELECT `+columnsOf[TriggerEvent]("event")+nextAdmissibleTriggerCapacitySQL,
		scope.UserID, scope.ProjectID, scope.WorkflowRef, limits.MaxRunning, limits.MaxRunningPerKey, limits.expiredBefore()).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("find the next admissible trigger capacity event: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return triggerEventToCore(rows[0])
}

// CanLaunchTriggerCapacity checks admission order and capacity inside the
// caller's launch transaction: eventID may launch only if it is the event
// NextAdmissibleTriggerCapacityEvent names under the scope's lock. The caller
// MUST claim the pending event in that same transaction before releasing the
// lock (ClaimPendingTriggerEvent already does this).
// Missing terminal evidence retains a slot, including half-starts, paused runs,
// deleted chats and unknown statuses. Only the Temporal-confirmed release flag
// frees capacity; best-effort chat/run status never releases a slot. The one
// exception is a run parked on a provider, which holds no workflow-wide slot
// while it waits (ParkTriggerCapacity).
//
// An admitted event is stamped with limits.MaxRunning: the cap a parked run
// resumes against (ResumeTriggerCapacity).
func CanLaunchTriggerCapacity(ctx context.Context, db orm.Context, eventID string, scope TriggerCapacityScope, limits TriggerCapacityLimits) (bool, error) {
	if !scope.valid() || eventID == "" || !limits.valid() {
		return false, errors.New("invalid trigger capacity policy")
	}
	if err := lockTriggerCapacity(ctx, db, scope); err != nil {
		return false, err
	}
	var next []string
	err := db.Bun().NewRaw(`SELECT event.id`+nextAdmissibleTriggerCapacitySQL,
		scope.UserID, scope.ProjectID, scope.WorkflowRef, limits.MaxRunning, limits.MaxRunningPerKey, limits.expiredBefore()).Scan(ctx, &next)
	if err != nil {
		return false, fmt.Errorf("check trigger capacity: %w", err)
	}
	allowed := len(next) == 1 && next[0] == eventID
	if allowed {
		if _, err := db.Bun().NewRaw(`UPDATE trigger_events SET capacity_limit = ? WHERE id = ?`, limits.MaxRunning, eventID).Exec(ctx); err != nil {
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
