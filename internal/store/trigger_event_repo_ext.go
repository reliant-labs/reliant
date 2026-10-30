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

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/uptrace/bun/dialect/pgdialect"

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
	return firstTriggerEvent(ctx, db, "get trigger event by dedupe key",
		orm.WhereEq(TriggerEventFieldKind, kind),
		orm.WhereEq(TriggerEventFieldDedupeKey, dedupeKey))
}

// GetTriggerEventByChatID returns the chat's launch event — the earliest
// event that references it — or core.ErrTriggerEventNotFound. Served by
// idx_trigger_events_chat.
func GetTriggerEventByChatID(ctx context.Context, db orm.Context, chatID string) (*core.TriggerEvent, error) {
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
	row := &TriggerEvent{ID: id, Outcome: outcome, OutcomeDetail: detail, ChatID: chatID}
	return writeTriggerEvent(ctx, db, row, "outcome",
		TriggerEventFieldOutcome, TriggerEventFieldOutcomeDetail, TriggerEventFieldChatID)
}

// UpdateTriggerEventPayload replaces a firing's payload, or returns
// core.ErrTriggerEventNotFound.
func UpdateTriggerEventPayload(ctx context.Context, db orm.Context, id string, payload map[string]any) error {
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

// The pending-event statements below spell `outcome = 'pending'` into the SQL
// rather than binding it: it is idx_trigger_events_pending's partial-index
// predicate, which the planner must see at plan time, and for the claim and
// settle it is the guard that makes them conditional.

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
	// left to recover, and redriving it forever would starve the heads.
	var rows []*TriggerEvent
	err := db.Bun().NewRaw(`WITH ordinary AS (
 SELECT id FROM trigger_events WHERE capacity_order IS NULL AND outcome = 'pending' AND created_at < ?0
 ORDER BY created_at,id LIMIT ?1
), heads AS (
 SELECT DISTINCT ON (user_id,capacity_project_id,capacity_workflow_ref) id,capacity_redriven_at,created_at
 FROM trigger_events WHERE capacity_order IS NOT NULL AND created_at < ?0
 AND outcome = 'pending'
 ORDER BY user_id,capacity_project_id,capacity_workflow_ref,capacity_order
), candidates AS (
 SELECT id,capacity_redriven_at,created_at FROM heads
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
ORDER BY event.created_at,event.id`, olderThan, limit).Scan(ctx, &rows)
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

// ClaimPendingTriggerEvent adopts an inbound event the receiver recorded: the
// row moves from pending to launched (chat attached later in the same
// transaction) and takes the launch's start record as its payload. The
// outcome predicate is what makes two concurrent fires of one event launch it
// once: the loser updates zero rows, and reports claimed=false.
func ClaimPendingTriggerEvent(ctx context.Context, db orm.Context, id string, payload map[string]any) (bool, error) {
	encoded, err := triggerJSONObject(payload)
	if err != nil {
		return false, fmt.Errorf("marshal trigger event payload: %w", err)
	}
	res, err := db.Bun().NewUpdate().Model((*TriggerEvent)(nil)).
		Set("outcome = 'launched'").
		Set("outcome_detail = ''").
		Set("payload = ?", string(encoded)).
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
func SettlePendingTriggerEvent(ctx context.Context, db orm.Context, id string, outcome core.TriggerEventOutcome, detail string) (bool, error) {
	res, err := db.Bun().NewUpdate().Model((*TriggerEvent)(nil)).
		Set("outcome = ?", outcome).
		Set("outcome_detail = ?", detail).
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

// listTriggerEventsSQL is one page of a trigger's firings, newest first,
// keyset on (occurred_at, id) so a firing recorded mid-pagination can neither
// repeat nor be skipped; two fires can share an occurred_at, and id breaks the
// tie. user_id scopes the rows even though the handler already checked
// ownership. Served by idx_trigger_events_trigger_occurred_id.
var listTriggerEventsSQL = `SELECT ` + columnsOf[TriggerEvent]("e") + `,
	` + triggerFiringRunColumns + `
FROM trigger_events e
LEFT JOIN chats_with_activity c ON c.id = e.chat_id AND c.user_id = e.user_id
LEFT JOIN workflows rw ON rw.id = c.workflow_id
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
func triggerOutcomeColumns(alias string) string {
	var b strings.Builder
	for i, f := range modelTables.Get(reflect.TypeFor[TriggerEvent]()).Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		name := string(f.SQLName)
		if triggerEventBodyColumns[name] {
			b.WriteString("NULL::jsonb AS " + name)
			continue
		}
		b.WriteString(alias + "." + name)
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
LEFT JOIN chats_with_activity c ON c.id = e.chat_id AND c.user_id = e.user_id
LEFT JOIN workflows rw ON rw.id = c.workflow_id
ORDER BY e.trigger_id, e.occurred_at DESC, e.id DESC`
}

var (
	recentTriggerFiringsFullSQL     = recentTriggerFiringsSQL(columnsOf[TriggerEvent])
	recentTriggerFiringsOutcomesSQL = recentTriggerFiringsSQL(triggerOutcomeColumns)
)

// InboxTriggerFiringsSQL returns the statements of RecentTriggerOutcomes and
// FiringsSinceLastSuccess, with bun's ?0 (user), ?1 (trigger ids) and ?2 (per
// trigger) placeholders, so a plan test runs the exact statements the Inbox
// does.
func InboxTriggerFiringsSQL() (recentOutcomes, sinceLastSuccess string) {
	return recentTriggerFiringsOutcomesSQL, firingsSinceLastSuccessSQL
}

// RecentTriggerFirings returns each named trigger's newest perTrigger firings
// (newest first, with their runs), keyed by trigger id. A trigger with no
// firings has no entry; triggers belonging to other users never appear.
func RecentTriggerFirings(ctx context.Context, db orm.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	return triggerFiringsByTrigger(ctx, db, "list recent trigger firings", recentTriggerFiringsFullSQL, userID, triggerIDs, perTrigger)
}

// RecentTriggerOutcomes is RecentTriggerFirings without the events' bodies:
// Payload is empty and Sender and Origin are nil. It is for a reader that
// judges health and never shows an event (the Inbox).
func RecentTriggerOutcomes(ctx context.Context, db orm.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	return triggerFiringsByTrigger(ctx, db, "list recent trigger outcomes", recentTriggerFiringsOutcomesSQL, userID, triggerIDs, perTrigger)
}

// firingsSinceLastSuccessSQL is every firing of each named trigger after its
// newest SUCCESS (a launched firing whose run completed), newest first, capped
// at ?2 rows. A trigger with no success returns its whole history up to the
// cap. This is the failure episode: Go reads its length and its oldest
// failure from it, so the episode is not truncated at the 10-firing health
// window.
//
// "Completed" is the run the trigger fired (run_status), not whatever turn the
// chat is on now — the same rule triggers.resolveFiring applies — so a
// person's later reply in the chat neither ends nor extends the episode.
//
// Per trigger, both halves walk idx_trigger_events_trigger_occurred_id newest
// first: the newest success stops at the first one found, and the episode at
// it or at ?2 rows. Finding them with DISTINCT ON and row_number() over the
// whole history judged every firing the triggers ever had (each joined to its
// run's display state) on every Inbox read.
var firingsSinceLastSuccessSQL = `SELECT ` + triggerOutcomeColumns("e") + `,
	` + triggerFiringRunColumns + `
FROM (SELECT DISTINCT unnest(?1::text[]) AS id) t
LEFT JOIN LATERAL (
	SELECT s.occurred_at, s.id
	FROM trigger_events s
	LEFT JOIN chats_with_activity sc ON sc.id = s.chat_id AND sc.user_id = s.user_id
	WHERE s.trigger_id = t.id
		AND s.user_id = ?0::text
		AND s.outcome = 'launched'
		AND (s.run_status = 'completed' OR (s.run_status IS NULL AND sc.display_state = 5))
	ORDER BY s.occurred_at DESC, s.id DESC
	LIMIT 1
) ls ON true
CROSS JOIN LATERAL (
	SELECT ` + triggerOutcomeColumns("x") + ` FROM trigger_events x
	WHERE x.trigger_id = t.id
		AND x.user_id = ?0::text
		AND (ls.id IS NULL OR (x.occurred_at, x.id) > (ls.occurred_at, ls.id))
	ORDER BY x.occurred_at DESC, x.id DESC
	LIMIT ?2::integer
) e
LEFT JOIN chats_with_activity c ON c.id = e.chat_id AND c.user_id = e.user_id
LEFT JOIN workflows rw ON rw.id = c.workflow_id
ORDER BY e.trigger_id, e.occurred_at DESC, e.id DESC`

// FiringsSinceLastSuccess returns, per named trigger, every firing after its
// newest success (newest first, with their runs, at most perTrigger), keyed by
// trigger id. An episode is counted, never shown, and runs to hundreds of
// firings, so the events carry no body: Payload is empty, Sender and Origin
// nil (triggerOutcomeColumns).
func FiringsSinceLastSuccess(ctx context.Context, db orm.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	return triggerFiringsByTrigger(ctx, db, "list firings since last success", firingsSinceLastSuccessSQL, userID, triggerIDs, perTrigger)
}

func triggerFiringsByTrigger(ctx context.Context, db orm.Context, what, query, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	out := make(map[string][]*core.TriggerEventWithRun, len(triggerIDs))
	if len(triggerIDs) == 0 || perTrigger <= 0 {
		return out, nil
	}
	var rows []triggerFiringRow
	if err := db.Bun().NewRaw(query, userID, pgdialect.Array(triggerIDs), orm.Arg(perTrigger)).Scan(ctx, &rows); err != nil {
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
	}, nil
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
		CapacityOrder:       row.CapacityOrder,
		CapacityProjectID:   row.CapacityProjectID,
		CapacityWorkflowRef: row.CapacityWorkflowRef,
	}
	if row.RunStatus != nil {
		ev.RunStatus = *row.RunStatus
	}
	return ev, nil
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
