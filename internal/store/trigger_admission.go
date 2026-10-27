package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// LockTriggerAdmission serializes decisions in a short transaction. It is not
// the run permit: the reserved event survives connection and process loss.
func LockTriggerAdmission(ctx context.Context, db orm.Context, scope string) error {
	_, err := db.Bun().NewRaw(`SELECT pg_advisory_xact_lock(hashtextextended(?0, 0))`, "trigger-admission:"+scope).Exec(ctx)
	return err
}

const admissionSelect = `SELECT id AS event_id, admission_scope, admission_key,
 COALESCE(admission_head,'') AS admission_head, admission_order, admission_state,
 COALESCE(admission_run_id,'') AS admission_run_id, COALESCE(admission_workflow_id,'') AS chat_id,
 COALESCE(superseded_by,'') AS superseded_by FROM trigger_events `

func GetAdmissionEvent(ctx context.Context, db orm.Context, id string) (*core.TriggerEvent, error) {
	row, err := GetTriggerEventByID(ctx, db, id)
	if err != nil {
		return nil, err
	}
	return triggerEventToCore(row)
}

func GetTriggerAdmission(ctx context.Context, db orm.Context, id string) (*core.TriggerAdmission, error) {
	var a core.TriggerAdmission
	err := db.Bun().NewRaw(admissionSelect+`WHERE id = ?0 AND admission_state IS NOT NULL`, id).Scan(ctx, &a)
	return &a, err
}

// EnqueueTriggerAdmission must run under a scope lock in a transaction. Exact
// head repeats attach to the existing intent across activations. Unknown heads
// only use source-delivery dedupe; they must not collapse unrelated events.
func EnqueueTriggerAdmission(ctx context.Context, db orm.Context, ev *core.TriggerEvent, req core.TriggerAdmissionRequest) (*core.TriggerEvent, bool, error) {
	if ev == nil || req.Scope == "" || len(req.Scope) > 512 || len(req.Key) == 0 || len(req.Key) > 512 || len(req.Head) > 256 || req.PendingLimit < 1 {
		return nil, false, fmt.Errorf("invalid automation admission request")
	}
	if err := LockTriggerAdmission(ctx, db, req.Scope); err != nil {
		return nil, false, err
	}
	existing, err := GetTriggerEventByDedupe(ctx, db, ev.Kind, ev.DedupeKey)
	if err == nil {
		return existing, false, nil
	}
	if err != core.ErrTriggerEventNotFound {
		return nil, false, err
	}
	if req.Head != "" {
		var ids []string
		err = db.Bun().NewRaw(`SELECT id FROM trigger_events WHERE admission_scope=?0 AND admission_key=?1 AND admission_head=?2 AND admission_state IN ('queued','reserved','released') ORDER BY admission_order DESC LIMIT 1`, req.Scope, req.Key, req.Head).Scan(ctx, &ids)
		if err != nil {
			return nil, false, err
		}
		if len(ids) > 0 {
			old, e := GetTriggerEventByID(ctx, db, ids[0])
			if e != nil {
				return nil, false, e
			}
			converted, e := triggerEventToCore(old)
			return converted, false, e
		}
	}
	var queued []core.TriggerAdmission
	if err = db.Bun().NewRaw(admissionSelect+`WHERE admission_scope=?0 AND admission_key=?1 AND admission_state='queued'`, req.Scope, req.Key).Scan(ctx, &queued); err != nil {
		return nil, false, err
	}
	state := "queued"
	if len(queued) == 0 {
		var count int
		if err = db.Bun().NewRaw(`SELECT count(*) FROM trigger_events WHERE admission_scope=?0 AND admission_state='queued'`, req.Scope).Scan(ctx, &count); err != nil {
			return nil, false, err
		}
		if count >= req.PendingLimit {
			state = "overloaded"
			ev.Outcome = core.TriggerEventFailed
			ev.OutcomeDetail = "automation admission overloaded: pending-key limit reached"
		}
	}
	created, err := InsertTriggerEventIfAbsent(ctx, db, ev)
	if err != nil || !created {
		return ev, created, err
	}
	// Clear the queued unique index before assigning the replacement; keep history.
	if state == "queued" {
		_, err = db.Bun().NewRaw(`UPDATE trigger_events SET admission_state='superseded', superseded_by=?2, outcome='skipped', outcome_detail='superseded by a newer queued intent' WHERE admission_scope=?0 AND admission_key=?1 AND admission_state='queued'`, req.Scope, req.Key, ev.ID).Exec(ctx)
		if err != nil {
			return nil, false, err
		}
		_, err = db.Bun().NewRaw(`UPDATE trigger_events SET superseded_by=?2 WHERE admission_scope=?0 AND admission_key=?1 AND admission_state='reserved'`, req.Scope, req.Key, ev.ID).Exec(ctx)
		if err != nil {
			return nil, false, err
		}
	}
	_, err = db.Bun().NewRaw(`UPDATE trigger_events SET admission_scope=?1, admission_key=?2, admission_head=NULLIF(?3,''), admission_state=?4, admission_order=DEFAULT WHERE id=?0`, ev.ID, req.Scope, req.Key, req.Head, state).Exec(ctx)
	if err != nil {
		return nil, false, err
	}
	if len(queued) > 0 && state == "queued" {
		_, err = db.Bun().NewRaw(`UPDATE trigger_events SET admission_order=?1 WHERE id=?0`, ev.ID, queued[0].Order).Exec(ctx)
	}
	return ev, true, err
}

// ReserveTriggerAdmission runs INSIDE the launch transaction. A deferral rolls
// back the attempted launch but leaves its original durable intent queued.
func ReserveTriggerAdmission(ctx context.Context, db orm.Context, id string, maxRunning int) error {
	a, err := GetTriggerAdmission(ctx, db, id)
	if err != nil {
		return err
	}
	if err = LockTriggerAdmission(ctx, db, a.Scope); err != nil {
		return err
	}
	a, err = GetTriggerAdmission(ctx, db, id)
	if err != nil {
		return err
	}
	if a.State == "reserved" {
		return nil
	}
	if a.State != "queued" {
		return core.ErrAdmissionDeferred
	}
	var live int
	if err = db.Bun().NewRaw(`SELECT count(*) FROM trigger_events WHERE admission_scope=?0 AND admission_state='reserved'`, a.Scope).Scan(ctx, &live); err != nil {
		return err
	}
	if maxRunning < 1 || live >= maxRunning {
		return core.ErrAdmissionDeferred
	}
	var eligible []string
	if err = db.Bun().NewRaw(`SELECT e.id FROM trigger_events e WHERE e.admission_scope=?0 AND e.admission_state='queued' AND NOT EXISTS (SELECT 1 FROM trigger_events r WHERE r.admission_scope=e.admission_scope AND r.admission_key=e.admission_key AND r.admission_state='reserved') ORDER BY e.admission_order, e.id LIMIT 1`, a.Scope).Scan(ctx, &eligible); err != nil {
		return err
	}
	if len(eligible) == 0 || eligible[0] != id {
		return core.ErrAdmissionDeferred
	}
	_, err = db.Bun().NewRaw(`UPDATE trigger_events SET admission_state='reserved' WHERE id=?0 AND admission_state='queued'`, id).Exec(ctx)
	return err
}

// BindTriggerAdmissionRun never replaces a generation's original execution.
func BindTriggerAdmissionRun(ctx context.Context, db orm.Context, id, runID string) error {
	if runID == "" {
		return fmt.Errorf("empty admission run id")
	}
	res, err := db.Bun().NewRaw(`UPDATE trigger_events SET admission_run_id=?1 WHERE id=?0 AND admission_state='reserved' AND (admission_run_id IS NULL OR admission_run_id=?1)`, id, runID).Exec(ctx)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return fmt.Errorf("admission run identity no longer matches")
	}
	return err
}

// ReleaseTriggerAdmission is a generation CAS, called only after Temporal
// confirms the exact root/run is terminal. Cancellation requests never call it.
func ReleaseTriggerAdmission(ctx context.Context, db orm.Context, id, chatID, runID string) (bool, error) {
	if runID == "" || chatID == "" {
		return false, nil
	}
	res, err := db.Bun().NewRaw(`UPDATE trigger_events SET admission_state='released' WHERE id=?0 AND admission_workflow_id=?1 AND admission_run_id=?2 AND admission_state='reserved'`, id, chatID, runID).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SettleOrphanQueuedAdmission never alters a reservation that won the race.
func SettleOrphanQueuedAdmission(ctx context.Context, db orm.Context, id string) error {
	_, err := db.Bun().NewRaw(`UPDATE trigger_events SET admission_state='superseded',outcome='failed',outcome_detail='activation deleted before admission' WHERE id=?0 AND trigger_id IS NULL AND admission_state='queued' AND outcome='pending'`, id).Exec(ctx)
	return err
}

func TriggerAdmissionHighWater(ctx context.Context, db orm.Context) (int64, error) {
	var high int64
	err := db.Bun().NewRaw(`SELECT COALESCE(max(admission_order),0) FROM trigger_events WHERE admission_state IN ('queued','reserved')`).Scan(ctx, &high)
	return high, err
}

func ListTriggerAdmissions(ctx context.Context, db orm.Context, after, through int64, limit int) ([]core.TriggerAdmission, error) {
	var rows []core.TriggerAdmission
	err := db.Bun().NewRaw(admissionSelect+`WHERE admission_state IN ('queued','reserved') AND admission_order > ?0 AND admission_order <= ?1 ORDER BY admission_order,id LIMIT ?2`, after, through, limit).Scan(ctx, &rows)
	return rows, err
}
