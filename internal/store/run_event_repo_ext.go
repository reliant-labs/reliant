// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// The run_events queries (P5b lane L2): the outbox workflow-event triggers
// fire from (internal/triggers/workflowevent), written in the transaction of
// the state change it reports. Functions take (ctx, orm.Context), so every
// write joins that transaction, and speak core.RunEvent: the jsonb payload is
// mapped to the core map at this boundary.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertRunEvent writes ev, doing nothing when its dedupe key already exists:
// the first row for a transition is the record, and an activity retry must
// not add a second. created reports whether this call wrote the row. A zero
// CreatedAt is stamped now.
func InsertRunEvent(ctx context.Context, db orm.Context, ev *core.RunEvent) (created bool, err error) {
	payload, err := runEventPayload(ev.Payload)
	if err != nil {
		return false, fmt.Errorf("marshal run event payload: %w", err)
	}
	createdAt := ev.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	res, err := db.Bun().NewInsert().
		Model(&RunEvent{
			ID:           ev.ID,
			UserID:       ev.UserID,
			ChatID:       ev.ChatID,
			WorkflowName: ev.WorkflowName,
			Outcome:      ev.Outcome,
			DedupeKey:    ev.DedupeKey,
			Payload:      payload,
			OccurredAt:   ev.OccurredAt,
			CreatedAt:    createdAt,
		}).
		ExcludeColumn(RunEventFieldClaimedUntil, RunEventFieldDispatchedAt).
		On("CONFLICT (dedupe_key) DO NOTHING").
		Returning("NULL").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to create run event: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to create run event: %w", err)
	}
	return n > 0, nil
}

// LoadRunEvent reads one event by id; a miss is core.ErrRunEventNotFound.
func LoadRunEvent(ctx context.Context, db orm.Context, id string) (*core.RunEvent, error) {
	row, err := GetRunEventByID(ctx, db, id)
	if err != nil {
		if svcerr.IsNotFound(err) {
			return nil, core.ErrRunEventNotFound
		}
		return nil, fmt.Errorf("failed to get run event: %w", err)
	}
	return runEventToCore(row)
}

// LoadRunEventByDedupe reads the event recorded for a transition; a miss is
// core.ErrRunEventNotFound.
func LoadRunEventByDedupe(ctx context.Context, db orm.Context, dedupeKey string) (*core.RunEvent, error) {
	row := new(RunEvent)
	err := db.Bun().NewSelect().Model(row).Where("dedupe_key = ?", dedupeKey).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrRunEventNotFound
		}
		return nil, fmt.Errorf("failed to get run event by dedupe key: %w", err)
	}
	return runEventToCore(row)
}

// HasEnabledTriggerOfKind reports whether the user owns an enabled trigger of
// the kind. The emitter asks before writing an outbox row, so users without
// workflow-event triggers never pay for one.
func HasEnabledTriggerOfKind(ctx context.Context, db orm.Context, userID string, kind core.TriggerKind) (bool, error) {
	has, err := db.Bun().NewSelect().Model((*Trigger)(nil)).
		Where("user_id = ?", userID).
		Where("kind = ?", string(kind)).
		Where("enabled").
		Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to check for %s triggers: %w", kind, err)
	}
	return has, nil
}

// LockChatForRunEvent row-locks the chat until the transaction ends. It
// serializes the "was the run already blocked?" check across concurrent
// approval and question creations in one chat. A missing chat is
// core.ErrChatNotFound.
func LockChatForRunEvent(ctx context.Context, db orm.Context, chatID string) error {
	var id string
	err := db.Bun().NewSelect().Model((*Chat)(nil)).
		Column("id").
		Where("id = ?", chatID).
		For("UPDATE").
		Scan(ctx, &id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrChatNotFound
		}
		return fmt.Errorf("failed to lock chat %s: %w", chatID, err)
	}
	return nil
}

// otherPendingBlockersSQL counts the chat's pending approvals and questions
// other than the one being created. Zero means that one is what blocks the
// run.
const otherPendingBlockersSQL = `
SELECT (
    (SELECT count(*) FROM approvals a
     WHERE a.chat_id = ?0 AND a.status = 1 AND a.id <> ?1)
  + (SELECT count(*) FROM questions q
     WHERE q.chat_id = ?0 AND q.status = 1 AND q.id <> ?1)
)::integer AS pending`

// CountOtherPendingBlockers counts the chat's pending approvals and
// questions other than excludeID.
func CountOtherPendingBlockers(ctx context.Context, db orm.Context, chatID, excludeID string) (int, error) {
	var n int
	if err := db.Bun().NewRaw(otherPendingBlockersSQL, chatID, excludeID).Scan(ctx, &n); err != nil {
		return 0, fmt.Errorf("failed to count pending blockers: %w", err)
	}
	return n, nil
}

// ClaimRunEvents leases up to max undispatched events whose lease is free or
// expired, oldest first. FOR UPDATE SKIP LOCKED in the inner select lets
// several relays drain the outbox at once without handing one row to two of
// them.
func ClaimRunEvents(ctx context.Context, db orm.Context, now, leaseUntil time.Time, max int) ([]*core.RunEvent, error) {
	if max <= 0 {
		return nil, nil
	}
	claimable := db.Bun().NewSelect().
		TableExpr("run_events AS r").
		Column("r.id").
		Where("r.dispatched_at IS NULL").
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("r.claimed_until IS NULL").WhereOr("r.claimed_until < ?", now)
		}).
		OrderExpr("r.created_at, r.id").
		Limit(int64(max)).
		For("UPDATE SKIP LOCKED")

	var rows []*RunEvent
	err := db.Bun().NewUpdate().
		Model((*RunEvent)(nil)).
		Set("claimed_until = ?", leaseUntil).
		Where("id IN (?)", claimable).
		Returning("*").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("failed to claim run events: %w", err)
	}
	out := make([]*core.RunEvent, 0, len(rows))
	for _, row := range rows {
		ev, err := runEventToCore(row)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// MarkRunEventDispatched records that the event's dispatch has started. It
// writes one column and reads nothing back; a missing event is a no-op.
func MarkRunEventDispatched(ctx context.Context, db orm.Context, id string, at time.Time) error {
	return ignoreNotFound(runEventRepo.UpdateMasked(ctx, db,
		&RunEvent{ID: id, DispatchedAt: &at}, []string{RunEventFieldDispatchedAt}, crud.NoReadBack()))
}

// DeleteDispatchedRunEventsBefore prunes events dispatched before cutoff,
// returning how many it removed.
func DeleteDispatchedRunEventsBefore(ctx context.Context, db orm.Context, cutoff time.Time) (int64, error) {
	res, err := db.Bun().NewDelete().Model((*RunEvent)(nil)).
		Where("dispatched_at IS NOT NULL").
		Where("dispatched_at < ?", cutoff).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to prune run events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to prune run events: %w", err)
	}
	return n, nil
}

// runEventPayload is the stored jsonb document: a nil map is the empty object
// the NOT NULL column requires.
func runEventPayload(m map[string]any) (json.RawMessage, error) {
	if m == nil {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(m)
}

func runEventToCore(row *RunEvent) (*core.RunEvent, error) {
	payload := map[string]any{}
	if len(row.Payload) > 0 {
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return nil, fmt.Errorf("unmarshal run event payload (event %s): %w", row.ID, err)
		}
	}
	return &core.RunEvent{
		ID:           row.ID,
		UserID:       row.UserID,
		ChatID:       row.ChatID,
		WorkflowName: row.WorkflowName,
		Outcome:      row.Outcome,
		DedupeKey:    row.DedupeKey,
		Payload:      payload,
		OccurredAt:   row.OccurredAt,
		DispatchedAt: row.DispatchedAt,
		CreatedAt:    row.CreatedAt,
	}, nil
}

// RunEventExtra carries fields on RunEvent that are NOT columns — computed
// values, transient state, anything derived rather than stored. RunEvent is
// embedded on RunEventExtra (see run_event_orm.go), so a field added here is a field on
// every RunEvent value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type RunEventExtra struct {
}
