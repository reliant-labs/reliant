// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for TriggerEvent. The generated trigger_event_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: triggerEventRepo (a *crud.Repo[TriggerEvent]) and the
//     package-level GetTriggerEventByID / ListTriggerEvent / CreateTriggerEvent / UpdateTriggerEvent / DeleteTriggerEvent
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     TriggerEvent struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindTriggerEventBy(ctx context.Context, db orm.Context, /* args */) ([]*TriggerEvent, error) {
//		var rows []*TriggerEvent
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("trigger_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveTriggerEvents(ctx context.Context, db orm.Context) ([]*TriggerEvent, error) {
//		return ListTriggerEvent(ctx, db, orm.WhereEq("trigger_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// defaultTriggerEventLimit bounds an unspecified limit. An unbounded history
// query is never what a caller wants here: the firing log grows once per
// scheduled tick forever.
const defaultTriggerEventLimit = 50

// InsertTriggerEventIfAbsent records a firing, reporting created=false when a
// row for its (kind, dedupe_key) already existed. DO NOTHING rather than DO
// UPDATE: the first row for a key is the authoritative record of intent, and
// a retry must not overwrite its outcome. "A chat starts exactly once" and "a
// retried scheduled fire launches once" are the same database constraint, and
// the affected-row count is how a caller learns which side of it it landed on.
func InsertTriggerEventIfAbsent(ctx context.Context, db orm.Context, ev *core.TriggerEvent) (bool, error) {
	if ev == nil {
		return false, fmt.Errorf("trigger event cannot be nil")
	}
	if ev.DedupeKey == "" {
		// An empty dedupe key would make every firing of this kind collide
		// with every other one, which is far worse than a missing row: the
		// second real firing would silently report "already launched".
		return false, fmt.Errorf("trigger event dedupe key cannot be empty")
	}
	row, err := triggerEventFromCore(ev)
	if err != nil {
		return false, err
	}
	res, err := db.Bun().NewInsert().Model(row).
		On("CONFLICT (kind, dedupe_key) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to create trigger event: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetTriggerEventByDedupe returns the firing recorded for (kind, dedupeKey),
// or core.ErrTriggerEventNotFound.
func GetTriggerEventByDedupe(ctx context.Context, db orm.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error) {
	if dedupeKey == "" {
		return nil, fmt.Errorf("trigger event dedupe key cannot be empty")
	}
	return firstTriggerEvent(ctx, db, "get trigger event by dedupe key",
		orm.WhereEq(TriggerEventFieldKind, kind),
		orm.WhereEq(TriggerEventFieldDedupeKey, dedupeKey))
}

// GetTriggerEventByChatID returns the chat's launch event — the earliest
// event that references it — or core.ErrTriggerEventNotFound. Served by
// idx_trigger_events_chat.
func GetTriggerEventByChatID(ctx context.Context, db orm.Context, chatID string) (*core.TriggerEvent, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return firstTriggerEvent(ctx, db, "get trigger event by chat id",
		orm.WhereEq(TriggerEventFieldChatID, chatID),
		orm.WithOrderBy(TriggerEventFieldCreatedAt, orm.Asc),
		orm.WithOrderBy(TriggerEventFieldID, orm.Asc))
}

// GetTriggerEventByChat returns the event of the given kind that launched a
// chat, or core.ErrTriggerEventNotFound. The oldest wins: a chat is launched
// once, and a later row naming it can only be a replay. Served by
// idx_trigger_events_chat.
func GetTriggerEventByChat(ctx context.Context, db orm.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error) {
	if chatID == "" {
		return nil, errors.New("chat ID cannot be empty")
	}
	return firstTriggerEvent(ctx, db, "get trigger event by chat",
		orm.WhereEq(TriggerEventFieldKind, kind),
		orm.WhereEq(TriggerEventFieldChatID, chatID),
		orm.WithOrderBy(TriggerEventFieldOccurredAt, orm.Asc),
		orm.WithOrderBy(TriggerEventFieldID, orm.Asc))
}

// GetLatestTriggerEvent returns the trigger's newest firing, of the given
// outcome when outcome is set, or (nil, nil) when it has none: "no events
// yet" is the ordinary state of a trigger that has never fired, and of the
// overlap check's first run. The overlap check ("is this trigger's previous
// run still going?") asks for the latest launched event; the detail view for
// the latest of any outcome.
func GetLatestTriggerEvent(ctx context.Context, db orm.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error) {
	if triggerID == "" {
		return nil, errors.New("trigger ID cannot be empty")
	}
	opts := []orm.QueryOption{orm.WhereEq(TriggerEventFieldTriggerID, triggerID)}
	if outcome != nil {
		opts = append(opts, orm.WhereEq(TriggerEventFieldOutcome, *outcome))
	}
	opts = append(opts,
		orm.WithOrderBy(TriggerEventFieldOccurredAt, orm.Desc),
		orm.WithOrderBy(TriggerEventFieldID, orm.Desc))
	ev, err := firstTriggerEvent(ctx, db, "get latest trigger event", opts...)
	if errors.Is(err, core.ErrTriggerEventNotFound) {
		return nil, nil
	}
	return ev, err
}

// firstTriggerEvent is the first event opts select, or
// core.ErrTriggerEventNotFound.
func firstTriggerEvent(ctx context.Context, db orm.Context, what string, opts ...orm.QueryOption) (*core.TriggerEvent, error) {
	rows, err := ListTriggerEvent(ctx, db, append(opts, orm.WithLimit(1))...)
	if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", what, err)
	}
	if len(rows) == 0 {
		return nil, core.ErrTriggerEventNotFound
	}
	return triggerEventToCore(rows[0])
}

// countLiveLaunchedRunsSQL counts the chats the user launched through this
// kind of event whose ROOT run is still live: pending (1), active (2), or
// stopped-because-paused (state 3, stop reason 3). The inner joins drop
// events whose chat was deleted or never got a root workflow, neither of
// which can be running.
const countLiveLaunchedRunsSQL = `SELECT count(*) FROM trigger_events e
JOIN chats c ON c.id = e.chat_id
JOIN workflows w ON w.id = c.workflow_id
WHERE e.user_id = ?0
  AND e.kind = ?1
  AND e.outcome = 'launched'
  AND (w.state IN (1, 2) OR (w.state = 3 AND w.stop_reason = 3))`

// CountLiveLaunchedRuns counts the user's chats launched by an event of the
// given kind whose root run is still live.
func CountLiveLaunchedRuns(ctx context.Context, db orm.Context, userID string, kind core.TriggerEventKind) (int, error) {
	var count int
	if err := db.Bun().NewRaw(countLiveLaunchedRunsSQL, userID, string(kind)).Scan(ctx, &count); err != nil {
		return 0, fmt.Errorf("failed to count live launched runs: %w", err)
	}
	return count, nil
}

// UpdateTriggerEventOutcome records a firing's outcome and the chat it
// launched (nil for none), or returns core.ErrTriggerEventNotFound.
func UpdateTriggerEventOutcome(ctx context.Context, db orm.Context, id string, outcome core.TriggerEventOutcome, detail string, chatID *string) error {
	if id == "" {
		return fmt.Errorf("trigger event ID cannot be empty")
	}
	row := &TriggerEvent{ID: id, Outcome: outcome, OutcomeDetail: detail, ChatID: chatID}
	return writeTriggerEvent(ctx, db, row, "outcome",
		TriggerEventFieldOutcome, TriggerEventFieldOutcomeDetail, TriggerEventFieldChatID)
}

// UpdateTriggerEventPayload replaces a firing's payload, or returns
// core.ErrTriggerEventNotFound.
func UpdateTriggerEventPayload(ctx context.Context, db orm.Context, id string, payload map[string]any) error {
	if id == "" {
		return fmt.Errorf("trigger event ID cannot be empty")
	}
	encoded, err := triggerJSONObject(payload)
	if err != nil {
		return fmt.Errorf("marshal trigger event payload: %w", err)
	}
	return writeTriggerEvent(ctx, db, &TriggerEvent{ID: id, Payload: encoded}, "payload", TriggerEventFieldPayload)
}

func writeTriggerEvent(ctx context.Context, db orm.Context, row *TriggerEvent, what string, fields ...string) error {
	err := UpdateTriggerEventMasked(ctx, db, row, fields, crud.NoReadBack())
	if errors.Is(err, svcerr.ErrNotFound) {
		return core.ErrTriggerEventNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to update trigger event %s: %w", what, err)
	}
	return nil
}

// setLaunchEventRunStatusSQL records how the run a trigger fired ended, on
// the chat's launch event (the same earliest-event rule as
// GetTriggerEventByChatID).
const setLaunchEventRunStatusSQL = `UPDATE trigger_events SET run_status = ?0::text
WHERE id = (
	SELECT l.id FROM trigger_events l
	WHERE l.chat_id = ?1::text
	ORDER BY l.created_at ASC, l.id ASC
	LIMIT 1
)`

// SetLaunchEventRunFailureDetail records the launch run's diagnostic on its
// own launch event, so a later human turn cannot change the failure health.
func SetLaunchEventRunFailureDetail(ctx context.Context, db orm.Context, chatID, detail string) error {
	_, err := db.Bun().NewRaw(`UPDATE trigger_events SET outcome_detail = ?0::text
WHERE id = (SELECT id FROM trigger_events WHERE chat_id = ?1::text ORDER BY created_at ASC, id ASC LIMIT 1)
AND outcome = 'launched'`, detail, chatID).Exec(ctx)
	return err
}

// SetLaunchEventRunStatus records status (a core.TriggerRun* value) on
// chatID's launch event. A chat with no launch event is not an error: there
// is nothing to record against.
func SetLaunchEventRunStatus(ctx context.Context, db orm.Context, chatID, status string) error {
	if _, err := db.Bun().NewRaw(setLaunchEventRunStatusSQL, status, chatID).Exec(ctx); err != nil {
		return fmt.Errorf("failed to record launch event run status: %w", err)
	}
	return nil
}

// SetLaunchEventRunOutcome records outcome (a core.RunOutcome* value) on
// chatID's launch event, beside its run_status (the same earliest-event rule).
// A chat with no launch event is not an error.
func SetLaunchEventRunOutcome(ctx context.Context, db orm.Context, chatID, outcome string) error {
	if _, err := db.Bun().NewRaw(`UPDATE trigger_events SET run_outcome = ?0::text
WHERE id = (SELECT id FROM trigger_events WHERE chat_id = ?1::text ORDER BY created_at ASC, id ASC LIMIT 1)`, outcome, chatID).Exec(ctx); err != nil {
		return fmt.Errorf("failed to record launch event run outcome: %w", err)
	}
	return nil
}

// The pending-event statements below spell `outcome = 'pending'` into the SQL
// rather than binding it: it is idx_trigger_events_pending's partial-index
// predicate, which the planner must see at plan time, and for the claim and
// settle it is the guard that makes them conditional.

// CapacityHeadRedriveInterval is how long a waiting capacity head whose scope
// freed no slot goes between redrives (ListStalePendingTriggerEvents): the
// safety net for capacity freed without a released or parked row to show it.
const CapacityHeadRedriveInterval = 5 * time.Minute

// ListStalePendingTriggerEvents returns inbound events recorded but not yet
// settled, older than the cutoff, oldest first: the fire workflow for them was
// never started (the receiver crashed between the insert and the start) or
// died. The redriver restarts their fires. Served by idx_trigger_events_pending.
func ListStalePendingTriggerEvents(ctx context.Context, db orm.Context, olderThan time.Time, limit int) ([]*core.TriggerEvent, error) {
	if limit <= 0 {
		limit = defaultTriggerEventLimit
	}
	// Ordinary delivery recovery has its own budget. Capacity recovery rotates
	// one head per scope by last-attempt time, so a large blocked pool cannot
	// monopolize every tenant's redrive or expiry sweep. An unbound launch is a
	// candidate only while it still holds its slot: a released one has nothing
	// left to recover, and redriving it forever would starve the heads. A
	// scope's head is in admission order (priority, then arrival); a head held
	// back by its key's running cap hands its turn on when its fire declines
	// (triggers.capacityWake.wakeNext), as the redriver does not know the cap.
	//
	// A head is due when its scope has changed since it was last redriven:
	// it was never redriven (a new head), or a run of its scope released its
	// slot or parked on a provider after that (trigger_events_capacity_freed).
	// A full scope that freed nothing would only decline its head again, and
	// each such fire is a Temporal workflow and a launch attempt: prod redrove
	// every waiting head every pass, ~2 a minute per scope, ~85% of them to
	// decline (2026-11-26). What frees capacity without a row saying so (a
	// raised max_running, a deleted run row) is caught by
	// CapacityHeadRedriveInterval, the safety net.
	var rows []*TriggerEvent
	err := db.Bun().NewRaw(`WITH ordinary AS (
 SELECT id FROM trigger_events WHERE capacity_order IS NULL AND outcome = 'pending' AND created_at < ?0
 ORDER BY created_at,id LIMIT ?1
), heads AS (
 SELECT DISTINCT ON (user_id,capacity_project_id,capacity_workflow_ref) id,user_id,capacity_project_id,capacity_workflow_ref,capacity_redriven_at,created_at
 FROM trigger_events WHERE capacity_order IS NOT NULL AND created_at < ?0
 AND outcome = 'pending'
 ORDER BY user_id,capacity_project_id,capacity_workflow_ref,COALESCE(capacity_priority,0) DESC,capacity_order
), due AS (
 SELECT h.id,h.capacity_redriven_at,h.created_at FROM heads h
 WHERE h.capacity_redriven_at IS NULL
 OR h.capacity_redriven_at < now() - make_interval(secs => ?2)
 OR EXISTS (
  SELECT 1 FROM trigger_events freed
  WHERE freed.user_id = h.user_id AND freed.capacity_project_id = h.capacity_project_id
  AND freed.capacity_workflow_ref = h.capacity_workflow_ref
  AND freed.capacity_order IS NOT NULL AND (freed.capacity_released_at IS NOT NULL OR freed.capacity_parked_at IS NOT NULL)
  AND GREATEST(freed.capacity_released_at, freed.capacity_parked_at) >= h.capacity_redriven_at)
), candidates AS (
 SELECT id,capacity_redriven_at,created_at FROM due
 UNION ALL
 SELECT id,capacity_redriven_at,created_at FROM trigger_events
 WHERE capacity_order IS NOT NULL AND outcome='launched' AND capacity_first_run_id IS NULL AND NOT capacity_released
 AND created_at < ?0
), selected AS (
 SELECT id FROM candidates ORDER BY capacity_redriven_at NULLS FIRST,created_at,id LIMIT ?1
), touched AS (
 UPDATE trigger_events SET capacity_redriven_at = now() WHERE id IN (SELECT id FROM selected) RETURNING id
)
SELECT `+columnsOf[TriggerEvent]("event")+` FROM trigger_events event WHERE event.id IN (SELECT id FROM ordinary UNION ALL SELECT id FROM touched)
ORDER BY event.created_at,event.id`, olderThan, limit, CapacityHeadRedriveInterval.Seconds()).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("failed to list pending trigger events: %w", err)
	}
	out := make([]*core.TriggerEvent, 0, len(rows))
	for _, row := range rows {
		ev, err := triggerEventToCore(row)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// ExpirePendingTriggerCapacityEvents settles as failed, with detail, the
// events still waiting for workflow capacity that were recorded before
// recordedBefore: at most limit of them, oldest first. It returns the events
// it settled, as they are now, so each can be accounted for (the redriver
// opens a trigger.fire span per event).
//
// It sweeps every waiting event, not the scope heads
// ListStalePendingTriggerEvents returns: a head is chosen by priority, so a
// low-priority event behind a steady stream of higher ones is never a head,
// and would otherwise never expire. A waiting event holds no slot, so settling
// one frees nothing. Conditional on pending, like SettlePendingTriggerEvent,
// so it never overwrites a launch that won a race.
func ExpirePendingTriggerCapacityEvents(ctx context.Context, db orm.Context, recordedBefore time.Time, detail string, limit int) ([]*core.TriggerEvent, error) {
	if limit <= 0 {
		limit = defaultTriggerEventLimit
	}
	var rows []*TriggerEvent
	err := db.Bun().NewRaw(`UPDATE trigger_events SET outcome = ?0, outcome_detail = ?1
WHERE id IN (
 SELECT id FROM trigger_events WHERE capacity_order IS NOT NULL AND outcome = 'pending' AND created_at < ?2
 ORDER BY created_at, id LIMIT ?3
) AND outcome = 'pending'
RETURNING `+columnsOf[TriggerEvent]("trigger_events"), core.TriggerEventFailed, detail, recordedBefore.UTC(), limit).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("failed to expire waiting trigger events: %w", err)
	}
	out := make([]*core.TriggerEvent, 0, len(rows))
	for _, row := range rows {
		ev, err := triggerEventToCore(row)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// ClaimPendingTriggerEvent adopts an inbound event the receiver recorded: the
// row moves from pending to launched (chat attached later in the same
// transaction) and takes the launch's start record as its payload. The
// outcome predicate is what makes two concurrent fires of one event launch it
// once: the loser updates zero rows, and reports claimed=false.
//
// def is the workflow definition the claiming fire resolved. It is written in
// the same guarded statement, so only the fire that wins the claim records
// what it launched against; nil keeps the definition recorded at intake.
func ClaimPendingTriggerEvent(ctx context.Context, db orm.Context, id string, payload map[string]any, def *core.TriggerEventDefinition) (bool, error) {
	encoded, err := triggerJSONObject(payload)
	if err != nil {
		return false, fmt.Errorf("marshal trigger event payload: %w", err)
	}
	definition, err := triggerEventDefinitionJSON(def)
	if err != nil {
		return false, err
	}
	res, err := db.Bun().NewUpdate().Model((*TriggerEvent)(nil)).
		Set("outcome = 'launched'").
		Set("outcome_detail = ''").
		Set("payload = ?", string(encoded)).
		Set("definition = COALESCE(?::jsonb, definition)", definition).
		Where("id = ?", id).
		Where("outcome = 'pending'").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to claim pending trigger event: %w", err)
	}
	return oneRowTouched(res)
}

// SettlePendingTriggerEvent records the verdict on an inbound event that did
// NOT launch. Conditional on pending, so it can never overwrite a launch that
// won a race; settled=false means it did not apply.
//
// def is the workflow definition the settling fire resolved, written under
// the same guard so a fire that lost the race never rewrites the winner's;
// nil keeps the definition already recorded.
func SettlePendingTriggerEvent(ctx context.Context, db orm.Context, id string, outcome core.TriggerEventOutcome, detail string, def *core.TriggerEventDefinition) (bool, error) {
	definition, err := triggerEventDefinitionJSON(def)
	if err != nil {
		return false, err
	}
	res, err := db.Bun().NewUpdate().Model((*TriggerEvent)(nil)).
		Set("outcome = ?", outcome).
		Set("outcome_detail = ?", detail).
		Set("definition = COALESCE(?::jsonb, definition)", definition).
		Where("id = ?", id).
		Where("outcome = 'pending'").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to settle pending trigger event: %w", err)
	}
	return oneRowTouched(res)
}

func oneRowTouched(res sql.Result) (bool, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// triggerFiringRow is a trigger_events row with the run it launched, as the
// three firing reads select it. The run columns are NULL unless the firing
// joined a chat that still exists.
type triggerFiringRow struct {
	TriggerEvent
	RunChatID         *string `bun:"run_chat_id"`
	RunTitle          *string `bun:"run_title"`
	RunRootState      *int32  `bun:"run_root_state"`
	RunRootStopReason *int32  `bun:"run_root_stop_reason"`
	RunDisplayState   int32   `bun:"run_display_state"`
}

// triggerFiringRunColumns is the run a firing launched, for the firing reads
// below. run_display_state is the SAME table as ListRuns' — keep the two in
// step. It is meaningful only when run_chat_id is set.
const triggerFiringRunColumns = `c.id AS run_chat_id,
	c.title AS run_title,
	rw.state AS run_root_state,
	rw.stop_reason AS run_root_stop_reason,
	COALESCE(c.display_state, 1)::integer AS run_display_state`

// triggerFiringRunJoin joins each firing e to the run it launched, as c (its
// chat) and rw (the chat's root workflow), for triggerFiringRunColumns.
//
// The chat is a LATERAL probe of chats_with_activity by id, LIMIT 1 (id is
// the key, so it is the same row), never a join the planner may turn into a
// hash: hashing the view computes every chat's activity in the database. In
// prod (reliant#2736) the episode read hashed all 42,738 chats, three Seq
// Scans and 230-330ms on every Inbox read, to join 1,100 firings.
const triggerFiringRunJoin = `LEFT JOIN LATERAL (
	SELECT r.id, r.title, r.workflow_id, r.display_state FROM chats_with_activity r
	WHERE r.id = e.chat_id AND r.user_id = e.user_id
	LIMIT 1
) c ON true
LEFT JOIN workflows rw ON rw.id = c.workflow_id`

// listTriggerEventsSQL is one page of a trigger's firings, newest first,
// keyset on (occurred_at, id) so a firing recorded mid-pagination can neither
// repeat nor be skipped; two fires can share an occurred_at, and id breaks the
// tie. user_id scopes the rows even though the handler already checked
// ownership. Served by idx_trigger_events_trigger_occurred_id.
var listTriggerEventsSQL = `SELECT ` + columnsOf[TriggerEvent]("e") + `,
	` + triggerFiringRunColumns + `
FROM trigger_events e
` + triggerFiringRunJoin + `
WHERE e.trigger_id = ?0::text
	AND e.user_id = ?1::text
	AND (cardinality(?2::text[]) = 0 OR e.outcome = ANY(?2::text[]))
	AND (?3::timestamptz IS NULL
	     OR (e.occurred_at, e.id) < (?3::timestamptz, ?4::text))
ORDER BY e.occurred_at DESC, e.id DESC
LIMIT ?5`

// ListTriggerEventsPage returns up to f.Limit of a trigger's firings, each
// with the run it launched, and whether more follow. It reads one row beyond
// the limit to learn that without a count.
func ListTriggerEventsPage(ctx context.Context, db orm.Context, f core.TriggerEventFilters) ([]*core.TriggerEventWithRun, bool, error) {
	if f.TriggerID == "" {
		return nil, false, fmt.Errorf("trigger ID cannot be empty")
	}
	if f.UserID == "" {
		return nil, false, fmt.Errorf("user ID cannot be empty")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultTriggerEventLimit
	}
	// Never nil: cardinality(NULL) is NULL, which would match no row.
	outcomes := make([]string, len(f.Outcomes))
	for i, o := range f.Outcomes {
		outcomes[i] = string(o)
	}
	var cursorAt *time.Time
	var cursorID *string
	if f.After != nil {
		cursorAt, cursorID = &f.After.OccurredAt, &f.After.ID
	}
	var rows []triggerFiringRow
	err := db.Bun().NewRaw(listTriggerEventsSQL,
		f.TriggerID, f.UserID, pgdialect.Array(outcomes), cursorAt, cursorID, orm.Arg(limit+1)).Scan(ctx, &rows)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list trigger events: %w", err)
	}
	events, err := triggerFiringsToCore(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	return events, hasMore, nil
}

// triggerEventBodyColumns are the trigger_events columns that hold what a
// firing DELIVERED — the event body, who sent it, where it came from — rather
// than what became of it. A webhook or provider-event body runs to tens of KB.
var triggerEventBodyColumns = map[string]bool{"payload": true, "sender": true, "origin": true}

// triggerOutcomeColumns is columnsOf[TriggerEvent] with the body columns read
// as NULL, for the reads that only judge outcomes (health, failure streaks).
// Selecting and decoding the bodies there was most of ListInbox's time in
// prod: one user's 250-firing episode was 1.8 MB of JSON, transferred and
// unmarshalled into maps on every read (2026-10-23). The columns are still
// named, so the row shape — and every prepared statement — is unchanged.
//
// The match is on the field's bare name: SQLName is quoted (`"payload"`), so
// matching it never nulled anything, and every Inbox read shipped the bodies
// anyway until reliant#2736 (4.7 MB per episode read for one user).
func triggerOutcomeColumns(alias string) string {
	var b strings.Builder
	for i, f := range modelTables.Get(reflect.TypeFor[TriggerEvent]()).Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		if triggerEventBodyColumns[f.Name] {
			b.WriteString("NULL::jsonb AS " + string(f.SQLName))
			continue
		}
		b.WriteString(alias + "." + string(f.SQLName))
	}
	return b.String()
}

// recentTriggerFiringsSQL is the newest ?2 firings of each named trigger, with
// their runs, in ONE query, selecting cols as the event's columns. This is
// what health and last_event are computed from, so listing N triggers costs
// one extra query rather than N.
//
// Each trigger's firings are read newest first from
// idx_trigger_events_trigger_occurred_id and stop at ?2: the cost is the
// rows returned. Ranking the whole history with row_number() instead read
// every firing the triggers ever had, and joined each to its run before
// keeping ten: 280ms for an automation with 20,000 runs, against 7ms.
//
// cols names the event's columns under an alias: the lateral read selects
// them as x's, and the outer select as e's.
func recentTriggerFiringsSQL(cols func(alias string) string) string {
	return `SELECT ` + cols("e") + `,
	` + triggerFiringRunColumns + `
FROM (SELECT DISTINCT unnest(?1::text[]) AS id) t
CROSS JOIN LATERAL (
	SELECT ` + cols("x") + ` FROM trigger_events x
	WHERE x.trigger_id = t.id AND x.user_id = ?0::text
	ORDER BY x.occurred_at DESC, x.id DESC
	LIMIT ?2::integer
) e
` + triggerFiringRunJoin + `
ORDER BY e.trigger_id, e.occurred_at DESC, e.id DESC`
}

// recentTriggerFiringsNewestBodySQL is recentTriggerFiringsSQL with the
// bodies (payload, sender, origin) and the definition read for each trigger's
// newest firing only, and NULL for the rest. Only the newest firing is ever
// shown (a trigger's last_event); the others are judged for health and
// failure detail, which read none of those columns. Reading them all was
// most of ListTriggers' CPU in prod (2026-11-27): 834 firings carrying 4.3 MB
// of payload JSON, each decoded into a map, for the ~90 that are rendered.
//
// The rank is computed over the capped walk (p), never beside it, as in
// inboxEpisodeSQL; a value the CASE drops is never detoasted, so the bodies
// of the other firings are neither decompressed nor sent.
var recentTriggerFiringsNewestBodySQL = `SELECT ` + columnsOf[TriggerEvent]("e") + `,
	` + triggerFiringRunColumns + `
FROM (SELECT DISTINCT unnest(?1::text[]) AS id) t
CROSS JOIN LATERAL (
	SELECT ` + newestBodyColumns("k") + ` FROM (
		SELECT ` + triggerEventColumnRefs("p") + `,
			row_number() OVER (ORDER BY p.occurred_at DESC, p.id DESC) AS newest_rank
		FROM (
			SELECT ` + columnsOf[TriggerEvent]("x") + ` FROM trigger_events x
			WHERE x.trigger_id = t.id AND x.user_id = ?0::text
			ORDER BY x.occurred_at DESC, x.id DESC
			LIMIT ?2::integer
		) p
	) k
) e
` + triggerFiringRunJoin + `
ORDER BY e.trigger_id, e.occurred_at DESC, e.id DESC`

// newestBodyColumns is every trigger_events column of alias, with the body
// columns and the definition kept only on the row whose newest_rank is 1.
func newestBodyColumns(alias string) string {
	var b strings.Builder
	for i, f := range modelTables.Get(reflect.TypeFor[TriggerEvent]()).Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		col := alias + "." + string(f.SQLName)
		if triggerEventBodyColumns[f.Name] || inboxEpisodeOmittedNames[f.Name] {
			b.WriteString("CASE WHEN " + alias + ".newest_rank = 1 THEN " + col + " END AS " + string(f.SQLName))
			continue
		}
		b.WriteString(col)
	}
	return b.String()
}

var recentTriggerFiringsOutcomesSQL = recentTriggerFiringsSQL(triggerOutcomeColumns)

// InboxTriggerFiringsSQL returns the statements of RecentTriggerOutcomes and
// FiringsSinceLastSuccess, with bun's ?0 (user), ?1 (trigger ids) and ?2 (per
// trigger) placeholders, so a plan test runs the exact statements the Inbox
// does.
func InboxTriggerFiringsSQL() (recentOutcomes, sinceLastSuccess string) {
	return recentTriggerFiringsOutcomesSQL, firingsSinceLastSuccessSQL
}

// RecentTriggerFirings returns each named trigger's newest perTrigger firings
// (newest first, with their runs), keyed by trigger id. A trigger with no
// firings has no entry; triggers belonging to other users never appear. Only
// each trigger's newest firing carries its body and definition (the one a
// trigger shows as its last event); the older ones are read as
// RecentTriggerOutcomes reads them, with an empty Payload and nil Sender,
// Origin and Definition (recentTriggerFiringsNewestBodySQL).
func RecentTriggerFirings(ctx context.Context, db orm.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, errors.New("user ID cannot be empty")
	}
	return triggerFiringsByTrigger(ctx, db, "list recent trigger firings", recentTriggerFiringsNewestBodySQL, userID, triggerIDs, perTrigger)
}

// RecentTriggerOutcomes is RecentTriggerFirings without the events' bodies:
// Payload is empty and Sender and Origin are nil. It is for a reader that
// judges health and never shows an event (the Inbox).
func RecentTriggerOutcomes(ctx context.Context, db orm.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return triggerFiringsByTrigger(ctx, db, "list recent trigger outcomes", recentTriggerFiringsOutcomesSQL, userID, triggerIDs, perTrigger)
}

// firingsSinceLastSuccessSQL is every firing of each named trigger after its
// newest SUCCESS (a launched firing whose run completed, and did not complete
// only because its failure path ran, nor into a declared skip), newest first, capped at ?2 rows. A trigger with no success returns its whole history up to the
// cap. This is the failure episode: Go reads its length and its oldest
// failure from it, so the episode is not truncated at the 10-firing health
// window.
//
// "Completed" is the run the trigger fired (run_status), not whatever turn the
// chat is on now — the same rule triggers.resolveFiring applies — so a
// person's later reply in the chat neither ends nor extends the episode. A run
// that completed with run_outcome failure_handled — a node failed and the run
// went on past it (reliant#2753) — is not a success: resolveRecordedRun
// counts it a failure, so it must not end the episode either, or the streak
// would restart at every handled failure. Nor is one that completed with
// run_outcome skipped — it declared it did not do its work this time:
// resolveRecordedRun counts it a skip, which neither ends nor extends the
// streak, so ending the episode at it would split one outage in two at every
// skip. A completed run with no outcome (recorded before run_outcome existed)
// is still a success.
//
// The outcome is read on the at most ?2 firings the success search already
// holds, so it filters those rows and changes nothing about the index walks
// below.
//
// Per trigger, both halves walk idx_trigger_events_trigger_occurred_id newest
// first, and each reads at most ?2 firings:
//
//   - The newest success is looked for only among the newest ?2 firings. A
//     success older than those changes nothing: the episode is then the
//     newest ?2 firings either way. Unbounded, the search walked the whole
//     history of a trigger that had not succeeded lately, probing the run of
//     every launched firing on the way.
//   - The episode's lower bound is an index condition, so its walk stops at
//     the success. Written as `ls.id IS NULL OR (…) > (…)` it was a filter
//     instead: the walk passed the success and read the rest of the history
//     only to discard it. In prod (reliant#2736) that was 5,161 firings per
//     trigger, every trigger_events row the user had, 164k buffers, on every
//     Inbox read. With no success the bound is (-infinity, ”), below every
//     firing (occurred_at is NOT NULL, ids are not empty).
//
// Finding the success and the episode with DISTINCT ON and row_number() over
// the whole history judged every firing the triggers ever had (each joined to
// its run's display state) on every Inbox read.
var firingsSinceLastSuccessSQL = triggerEpisodeSQL("", "")

// Both the success boundary and returned rows exclude safely managed actors
// (the AttentionManagedChats predicate). Filtering after LIMIT/cutoff would let
// a managed success erase an unmanaged failure, or a long managed streak push
// it out of the episode window. An unfinished observer has no durable backstop.
// The success search filters inside its ?2 window, so it is still the newest
// ?2 unmanaged firings the episode itself can reach.
var firingsSinceLastUnmanagedSuccessSQL = triggerEpisodeSQL(unmanagedAttentionFiringSQL("s"), unmanagedAttentionFiringSQL("x"))

func unmanagedAttentionFiringSQL(alias string) string {
	return ` AND NOT (` + alias + `.outcome = 'launched' AND EXISTS (
		SELECT 1 FROM attention_actors a
		JOIN chats ac ON ac.id = a.chat_id AND ac.user_id = a.user_id AND ac.project_id = a.project_id
		JOIN projects ap ON ap.id = ac.project_id AND ap.user_id = ac.user_id
		WHERE a.chat_id = ` + alias + `.chat_id AND a.user_id = ` + alias + `.user_id
		  AND (a.finished OR (a.start_seen AND a.start_state IN ('recovering', 'action_required')
		    AND EXISTS (SELECT 1 FROM attention_incidents i
		      WHERE i.user_id = a.user_id AND i.project_id = a.project_id AND i.incident_key = a.incident_key
		        AND i.state IN ('recovering', 'action_required'))))
	))`
}

func triggerEpisodeSQL(successFilter, firingFilter string) string {
	return `SELECT ` + triggerOutcomeColumns("e") + `,
	` + triggerFiringRunColumns + `
FROM (SELECT DISTINCT unnest(?1::text[]) AS id) t
LEFT JOIN LATERAL (
	SELECT w.occurred_at, w.id
	FROM (
		SELECT s.occurred_at, s.id, s.outcome, s.run_status, s.run_outcome, s.chat_id, s.user_id
		FROM trigger_events s
		WHERE s.trigger_id = t.id AND s.user_id = ?0::text` + successFilter + `
		ORDER BY s.occurred_at DESC, s.id DESC
		LIMIT ?2::integer
	) w
	WHERE w.outcome = 'launched'
		AND ((w.run_status = 'completed' AND (w.run_outcome IS NULL OR w.run_outcome NOT IN ('failure_handled', 'skipped')))
			OR (w.run_status IS NULL AND (
				SELECT sc.display_state FROM chats_with_activity sc
				WHERE sc.id = w.chat_id AND sc.user_id = w.user_id) = 5))
	ORDER BY w.occurred_at DESC, w.id DESC
	LIMIT 1
) ls ON true
CROSS JOIN LATERAL (
	SELECT ` + triggerOutcomeColumns("x") + ` FROM trigger_events x
	WHERE x.trigger_id = t.id
		AND x.user_id = ?0::text
		AND (x.occurred_at, x.id) > (COALESCE(ls.occurred_at, '-infinity'::timestamptz), COALESCE(ls.id, ''))` + firingFilter + `
	ORDER BY x.occurred_at DESC, x.id DESC
	LIMIT ?2::integer
) e
` + triggerFiringRunJoin + `
ORDER BY e.trigger_id, e.occurred_at DESC, e.id DESC`
}

// inboxEpisodeSQL is triggerEpisodeSQL for the Inbox, returning only the
// firings the Inbox's derivation can read. The same ?2-capped walk, the same
// success boundary and the same order: only the rows returned shrink.
//
// The Inbox reads an episode in three ways (handlers/inbox failingAutomations),
// and a skipped firing past the first ?3 (triggers.HealthWindow) skipped ones
// changes none of them:
//
//   - triggers.FailureStreak: a skipped firing neither extends nor ends a
//     streak (resolveFiring reads it as skipped or unresolved).
//   - the firings with a run: the attention-managed chats and the newest run
//     (LastRunChatId). A skipped firing that carries a chat is kept.
//   - triggers.ComputeHealth over the first ?3 firings, after the Inbox drops
//     its managed launched ones (unmanagedFirings). It never drops a skipped
//     firing, so one with ?3 skipped firings ahead of it is never in that
//     window, and every firing that is in it is still returned, in order.
//
// A long failure episode is mostly the skipped firings of an event trigger
// whose filter declined every event since its last success, so this cuts a
// trigger from up to ?2 rows, each decoded and probed for its run, to the
// window plus its failures and launches. Neither read shows a firing's
// definition, so that is not read either.
//
// The success search (ls) and the episode walk (x) are triggerEpisodeSQL's,
// with the same filters: the boundary, which managed successes the filter
// passes over, and the ?2 cap are unchanged, so every firing kept is one
// FiringsSinceLastUnmanagedSuccess (or FiringsSinceLastSuccess) returns.
//
// The rank is computed over the capped walk (p), never beside it: a window
// function at the walk's own level would rank the whole history after the
// boundary before LIMIT applied.
func inboxEpisodeSQL(successFilter, firingFilter string) string {
	return `SELECT ` + triggerEventColumnRefs("e") + `,
	` + triggerFiringRunColumns + `
FROM (SELECT DISTINCT unnest(?1::text[]) AS id) t
LEFT JOIN LATERAL (
	SELECT w.occurred_at, w.id
	FROM (
		SELECT s.occurred_at, s.id, s.outcome, s.run_status, s.run_outcome, s.chat_id, s.user_id
		FROM trigger_events s
		WHERE s.trigger_id = t.id AND s.user_id = ?0::text` + successFilter + `
		ORDER BY s.occurred_at DESC, s.id DESC
		LIMIT ?2::integer
	) w
	WHERE w.outcome = 'launched'
		AND ((w.run_status = 'completed' AND (w.run_outcome IS NULL OR w.run_outcome <> 'failure_handled'))
			OR (w.run_status IS NULL AND (
				SELECT sc.display_state FROM chats_with_activity sc
				WHERE sc.id = w.chat_id AND sc.user_id = w.user_id) = 5))
	ORDER BY w.occurred_at DESC, w.id DESC
	LIMIT 1
) ls ON true
CROSS JOIN LATERAL (
	SELECT ` + triggerEventColumnRefs("k") + ` FROM (
		SELECT ` + triggerEventColumnRefs("p") + `,
			count(*) FILTER (WHERE p.outcome = 'skipped')
				OVER (ORDER BY p.occurred_at DESC, p.id DESC) AS skipped_rank
		FROM (
			SELECT ` + inboxEpisodeColumns("x") + ` FROM trigger_events x
			WHERE x.trigger_id = t.id
				AND x.user_id = ?0::text
				AND (x.occurred_at, x.id) > (COALESCE(ls.occurred_at, '-infinity'::timestamptz), COALESCE(ls.id, ''))` + firingFilter + `
			ORDER BY x.occurred_at DESC, x.id DESC
			LIMIT ?2::integer
		) p
	) k
	WHERE k.outcome IS DISTINCT FROM 'skipped' OR k.chat_id IS NOT NULL OR k.skipped_rank <= ?3::integer
) e
` + triggerFiringRunJoin + `
ORDER BY e.trigger_id, e.occurred_at DESC, e.id DESC`
}

var (
	inboxEpisodesSQL         = inboxEpisodeSQL(unmanagedAttentionFiringSQL("s"), unmanagedAttentionFiringSQL("x"))
	inboxEpisodeFailuresSQL  = inboxEpisodeSQL("", "")
	inboxEpisodeOmittedNames = map[string]bool{"definition": true}
)

// triggerEventColumnRefs is every trigger_events column of alias, by name: an
// outer query re-selecting what an inner one already projected.
func triggerEventColumnRefs(alias string) string {
	var b strings.Builder
	for i, f := range modelTables.Get(reflect.TypeFor[TriggerEvent]()).Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(alias + "." + string(f.SQLName))
	}
	return b.String()
}

// inboxEpisodeColumns is triggerOutcomeColumns without the definition as
// well: the Inbox counts an episode and never shows which definition a
// firing ran.
func inboxEpisodeColumns(alias string) string {
	var b strings.Builder
	for i, f := range modelTables.Get(reflect.TypeFor[TriggerEvent]()).Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		if triggerEventBodyColumns[f.Name] || inboxEpisodeOmittedNames[f.Name] {
			b.WriteString("NULL::jsonb AS " + string(f.SQLName))
			continue
		}
		b.WriteString(alias + "." + string(f.SQLName))
	}
	return b.String()
}

// InboxEpisodes is FiringsSinceLastUnmanagedSuccess as the Inbox reads it
// (inboxEpisodeSQL): the same episode, without the skipped firings past the
// first window that cannot change what the Inbox derives, and without
// definitions. window is the Inbox's health window (triggers.HealthWindow).
func InboxEpisodes(ctx context.Context, db orm.Context, userID string, triggerIDs []string, perTrigger, window int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return triggerFiringsByTrigger(ctx, db, "list the inbox's failure episodes", inboxEpisodesSQL, userID, triggerIDs, perTrigger, orm.Arg(window))
}

// InboxEpisodeFailures is FiringsSinceLastSuccess as the Inbox reads it, for
// the diagnostic failure count only (triggers.FailureStreak(…).Count), which
// no skipped firing changes: none is returned unless it carries a chat.
func InboxEpisodeFailures(ctx context.Context, db orm.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return triggerFiringsByTrigger(ctx, db, "list the inbox's diagnostic failure episodes", inboxEpisodeFailuresSQL, userID, triggerIDs, perTrigger, orm.Arg(0))
}

// FiringsSinceLastUnmanagedSuccess returns the legacy inbox episode, excluding
// registered attention-managed launched chats before both the cutoff and cap.
// RecentTriggerOutcomes and FiringsSinceLastSuccess remain diagnostic reads.
func FiringsSinceLastUnmanagedSuccess(ctx context.Context, db orm.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return triggerFiringsByTrigger(ctx, db, "list unmanaged firings since last success", firingsSinceLastUnmanagedSuccessSQL, userID, triggerIDs, perTrigger)
}

// FiringsSinceLastSuccess returns, per named trigger, every firing after its
// newest success (newest first, with their runs, at most perTrigger), keyed by
// trigger id. An episode is counted, never shown, and runs to hundreds of
// firings, so the events carry no body: Payload is empty, Sender and Origin
// nil (triggerOutcomeColumns).
func FiringsSinceLastSuccess(ctx context.Context, db orm.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return triggerFiringsByTrigger(ctx, db, "list firings since last success", firingsSinceLastSuccessSQL, userID, triggerIDs, perTrigger)
}

// extra binds ?3 onward, for a statement that takes more than the three.
func triggerFiringsByTrigger(ctx context.Context, db orm.Context, what, query, userID string, triggerIDs []string, perTrigger int, extra ...any) (map[string][]*core.TriggerEventWithRun, error) {
	out := make(map[string][]*core.TriggerEventWithRun, len(triggerIDs))
	if len(triggerIDs) == 0 || perTrigger <= 0 {
		return out, nil
	}
	var rows []triggerFiringRow
	// Every value binds (orm.Arg; the user id binds as a string), so each
	// statement is one prepared statement and one cached plan whatever the
	// user or triggers. pgdialect.Array renders itself and stayed inline,
	// which made the text, and so the prepared statement, new for each set
	// of trigger ids: a Parse and a fresh plan on ListInbox's two slowest
	// reads (1.06s and 1.03s in prod trace ed2301a0…).
	if err := db.Bun().NewRaw(query, append([]any{userID, orm.Arg(triggerIDs), orm.Arg(perTrigger)}, extra...)...).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to %s: %w", what, err)
	}
	firings, err := triggerFiringsToCore(rows)
	if err != nil {
		return nil, err
	}
	for _, f := range firings {
		if f.Event.TriggerID == nil {
			continue
		}
		out[*f.Event.TriggerID] = append(out[*f.Event.TriggerID], f)
	}
	return out, nil
}

func triggerFiringsToCore(rows []triggerFiringRow) ([]*core.TriggerEventWithRun, error) {
	out := make([]*core.TriggerEventWithRun, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		ev, err := triggerEventToCore(&row.TriggerEvent)
		if err != nil {
			return nil, err
		}
		firing := &core.TriggerEventWithRun{Event: ev}
		if row.RunChatID != nil {
			firing.Run = &core.TriggerEventRun{
				ChatID:       *row.RunChatID,
				DisplayState: core.RunDisplayState(row.RunDisplayState),
			}
			if row.RunTitle != nil {
				firing.Run.Title = *row.RunTitle
			}
			if row.RunRootState != nil {
				firing.Run.RootStatus = core.WorkflowStatus{State: core.WorkflowState(*row.RunRootState)}
				if row.RunRootStopReason != nil {
					firing.Run.RootStatus.StopReason = core.WorkflowStopReason(*row.RunRootStopReason)
				}
			}
		}
		out = append(out, firing)
	}
	return out, nil
}

func triggerEventFromCore(ev *core.TriggerEvent) (*TriggerEvent, error) {
	payload, err := triggerJSONObject(ev.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshal trigger event payload: %w", err)
	}
	// sender and origin are NULL for none.
	var sender, origin json.RawMessage
	if ev.Sender != nil {
		if sender, err = json.Marshal(ev.Sender); err != nil {
			return nil, fmt.Errorf("marshal trigger event sender: %w", err)
		}
	}
	if ev.Origin != nil {
		if origin, err = json.Marshal(ev.Origin); err != nil {
			return nil, fmt.Errorf("marshal trigger event origin: %w", err)
		}
	}
	definition, err := triggerEventDefinitionJSON(ev.Definition)
	if err != nil {
		return nil, err
	}
	return &TriggerEvent{
		ID:            ev.ID,
		TriggerID:     ev.TriggerID,
		UserID:        ev.UserID,
		Kind:          ev.Kind,
		DedupeKey:     ev.DedupeKey,
		OccurredAt:    ev.OccurredAt,
		Payload:       payload,
		Outcome:       ev.Outcome,
		OutcomeDetail: ev.OutcomeDetail,
		ChatID:        ev.ChatID,
		CreatedAt:     ev.CreatedAt,
		Sender:        sender,
		Origin:        origin,
		Definition:    definition,
	}, nil
}

// triggerEventDefinitionJSON is a firing's definition as stored: NULL for none.
func triggerEventDefinitionJSON(def *core.TriggerEventDefinition) (json.RawMessage, error) {
	if def == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(def)
	if err != nil {
		return nil, fmt.Errorf("marshal trigger event definition: %w", err)
	}
	return encoded, nil
}

func triggerEventToCore(row *TriggerEvent) (*core.TriggerEvent, error) {
	payload := map[string]any{}
	if err := triggerJSONInto(row.Payload, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal trigger event payload (event %s): %w", row.ID, err)
	}
	var sender *core.TriggerSender
	if len(row.Sender) > 0 {
		sender = &core.TriggerSender{}
		if err := json.Unmarshal(row.Sender, sender); err != nil {
			return nil, fmt.Errorf("unmarshal trigger event sender (event %s): %w", row.ID, err)
		}
	}
	var origin *core.TriggerOrigin
	if len(row.Origin) > 0 {
		origin = &core.TriggerOrigin{}
		if err := json.Unmarshal(row.Origin, origin); err != nil {
			return nil, fmt.Errorf("unmarshal trigger event origin (event %s): %w", row.ID, err)
		}
	}
	var definition *core.TriggerEventDefinition
	if len(row.Definition) > 0 {
		definition = &core.TriggerEventDefinition{}
		if err := json.Unmarshal(row.Definition, definition); err != nil {
			return nil, fmt.Errorf("unmarshal trigger event definition (event %s): %w", row.ID, err)
		}
	}
	ev := &core.TriggerEvent{
		ID:                  row.ID,
		TriggerID:           row.TriggerID,
		UserID:              row.UserID,
		Kind:                row.Kind,
		DedupeKey:           row.DedupeKey,
		OccurredAt:          row.OccurredAt,
		Payload:             payload,
		Outcome:             row.Outcome,
		OutcomeDetail:       row.OutcomeDetail,
		ChatID:              row.ChatID,
		CreatedAt:           row.CreatedAt,
		Sender:              sender,
		Origin:              origin,
		Definition:          definition,
		CapacityOrder:       row.CapacityOrder,
		CapacityProjectID:   row.CapacityProjectID,
		CapacityWorkflowRef: row.CapacityWorkflowRef,
		CapacityKey:         row.CapacityKey,
		CapacityRunningKey:  row.CapacityRunningKey,
		CapacityPriority:    row.CapacityPriority,
	}
	if row.RunStatus != nil {
		ev.RunStatus = *row.RunStatus
	}
	if row.RunOutcome != nil {
		ev.RunOutcome = *row.RunOutcome
	}
	return ev, nil
}

// priorTriggerFailuresSQL is, for each (trigger, since, before) triple, the
// time of the trigger's newest FAILED firing in [since, before): ?1 the
// trigger ids, ?2 and ?3 the bounds, in the same order, ?0 the user. A
// failed firing is one that could not launch, or one whose run failed or
// completed only because its failure path ran (resolveRecordedRun's rule); a
// skipped run, or one still going, is not. ord is the triple's 1-based
// position, so the same trigger may be asked about at several bounds.
//
// Each lateral walks idx_trigger_events_trigger_occurred_id newest first from
// before down to since, and stops at the first failure: at most the
// trigger's firings of one window.
const priorTriggerFailuresSQL = `SELECT t.ord::integer AS ord, f.occurred_at
FROM unnest(?1::text[], ?2::timestamptz[], ?3::timestamptz[]) WITH ORDINALITY AS t(id, since, before, ord)
CROSS JOIN LATERAL (
	SELECT x.occurred_at FROM trigger_events x
	WHERE x.trigger_id = t.id AND x.user_id = ?0::text
		AND x.occurred_at >= t.since AND x.occurred_at < t.before
		AND (x.outcome = 'failed'
			OR (x.outcome = 'launched' AND (x.run_status = 'failed' OR x.run_outcome = 'failure_handled')))
	ORDER BY x.occurred_at DESC, x.id DESC
	LIMIT 1
) f`

// PriorTriggerFailuresSQL returns PriorTriggerFailures' statement, with bun's
// ?0..?3 placeholders, so a plan test runs the exact statement the Inbox does.
func PriorTriggerFailuresSQL() string { return priorTriggerFailuresSQL }

// PriorTriggerFailures returns, for each triggerIDs[i], when its newest failed
// firing in the window before before[i] occurred: in [before[i]-window,
// before[i]), nil when it had none. The result is aligned with triggerIDs; a
// trigger may be named more than once, at different bounds. It is what tells
// a failure that begins a new streak after a quiet spell from one more of an
// automation that keeps failing (triggers.Habitual).
func PriorTriggerFailures(ctx context.Context, db orm.Context, userID string, triggerIDs []string, before []time.Time, window time.Duration) ([]*time.Time, error) {
	if userID == "" {
		return nil, errors.New("user ID cannot be empty")
	}
	if len(triggerIDs) != len(before) {
		return nil, fmt.Errorf("prior trigger failures: %d trigger ids, %d bounds", len(triggerIDs), len(before))
	}
	out := make([]*time.Time, len(triggerIDs))
	if len(triggerIDs) == 0 {
		return out, nil
	}
	since := make([]time.Time, len(before))
	for i, b := range before {
		since[i] = b.Add(-window)
	}
	var rows []struct {
		Ord        int       `bun:"ord"`
		OccurredAt time.Time `bun:"occurred_at"`
	}
	// Every value binds (orm.Arg), as in triggerFiringsByTrigger: one
	// prepared statement whatever the user, triggers or bounds.
	if err := db.Bun().NewRaw(priorTriggerFailuresSQL, userID, orm.Arg(triggerIDs), orm.Arg(since), orm.Arg(before)).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list prior trigger failures: %w", err)
	}
	for _, r := range rows {
		if r.Ord < 1 || r.Ord > len(out) {
			continue
		}
		at := r.OccurredAt
		out[r.Ord-1] = &at
	}
	return out, nil
}

// TriggerEventExtra carries fields on TriggerEvent that are NOT columns — computed
// values, transient state, anything derived rather than stored. TriggerEvent is
// embedded on TriggerEventExtra (see trigger_event_orm.go), so a field added here is a field on
// every TriggerEvent value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type TriggerEventExtra struct {
}
