package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type runEventStore struct{ q pgdb.Querier }

// NewRunEventStore creates the Postgres run-event outbox implementation.
func NewRunEventStore(q pgdb.Querier) core.RunEventStore { return &runEventStore{q: q} }

func (s *runEventStore) CreateRunEvent(ctx context.Context, ev *core.RunEvent) (bool, error) {
	payload, err := triggerMapToJSON(ev.Payload)
	if err != nil {
		return false, fmt.Errorf("marshal run event payload: %w", err)
	}
	createdAt := ev.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	affected, err := s.q.CreateRunEvent(ctx, pgdb.CreateRunEventParams{
		ID:           ev.ID,
		UserID:       ev.UserID,
		ChatID:       ev.ChatID,
		WorkflowName: ev.WorkflowName,
		Outcome:      string(ev.Outcome),
		DedupeKey:    ev.DedupeKey,
		Payload:      payload,
		OccurredAt:   ev.OccurredAt,
		CreatedAt:    createdAt,
	})
	if err != nil {
		return false, fmt.Errorf("failed to create run event: %w", err)
	}
	return affected > 0, nil
}

func (s *runEventStore) GetRunEvent(ctx context.Context, id string) (*core.RunEvent, error) {
	row, err := s.q.GetRunEvent(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrRunEventNotFound
		}
		return nil, fmt.Errorf("failed to get run event: %w", err)
	}
	return runEventFromPG(row)
}

func (s *runEventStore) GetRunEventByDedupe(ctx context.Context, dedupeKey string) (*core.RunEvent, error) {
	row, err := s.q.GetRunEventByDedupe(ctx, dedupeKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrRunEventNotFound
		}
		return nil, fmt.Errorf("failed to get run event by dedupe key: %w", err)
	}
	return runEventFromPG(row)
}

func (s *runEventStore) HasEnabledTriggerOfKind(ctx context.Context, userID string, kind core.TriggerKind) (bool, error) {
	has, err := s.q.HasEnabledTriggerOfKind(ctx, pgdb.HasEnabledTriggerOfKindParams{UserID: userID, Kind: string(kind)})
	if err != nil {
		return false, fmt.Errorf("failed to check for %s triggers: %w", kind, err)
	}
	return has, nil
}

func (s *runEventStore) LockChatForRunEvent(ctx context.Context, chatID string) error {
	if _, err := s.q.LockChatForRunEvent(ctx, chatID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrChatNotFound
		}
		return fmt.Errorf("failed to lock chat %s: %w", chatID, err)
	}
	return nil
}

func (s *runEventStore) CountOtherPendingBlockers(ctx context.Context, chatID, excludeID string) (int, error) {
	n, err := s.q.CountOtherPendingBlockers(ctx, pgdb.CountOtherPendingBlockersParams{ChatID: chatID, ExcludeID: excludeID})
	if err != nil {
		return 0, fmt.Errorf("failed to count pending blockers: %w", err)
	}
	return int(n), nil
}

func (s *runEventStore) ClaimRunEvents(ctx context.Context, now, leaseUntil time.Time, max int) ([]*core.RunEvent, error) {
	rows, err := s.q.ClaimRunEvents(ctx, pgdb.ClaimRunEventsParams{ClaimedUntil: leaseUntil, Now: now, Max: int32(max)})
	if err != nil {
		return nil, fmt.Errorf("failed to claim run events: %w", err)
	}
	out := make([]*core.RunEvent, 0, len(rows))
	for _, row := range rows {
		ev, err := runEventFromPG(row)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func (s *runEventStore) MarkRunEventDispatched(ctx context.Context, id string, at time.Time) error {
	return s.q.MarkRunEventDispatched(ctx, pgdb.MarkRunEventDispatchedParams{ID: id, DispatchedAt: sql.NullTime{Time: at, Valid: true}})
}

func (s *runEventStore) DeleteDispatchedRunEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	n, err := s.q.DeleteDispatchedRunEventsBefore(ctx, sql.NullTime{Time: cutoff, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("failed to prune run events: %w", err)
	}
	return n, nil
}

func runEventFromPG(row pgdb.RunEvent) (*core.RunEvent, error) {
	payload := map[string]any{}
	if err := triggerJSONToMap(row.Payload, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal run event payload (event %s): %w", row.ID, err)
	}
	ev := &core.RunEvent{
		ID:           row.ID,
		UserID:       row.UserID,
		ChatID:       row.ChatID,
		WorkflowName: row.WorkflowName,
		Outcome:      core.RunEventOutcome(row.Outcome),
		DedupeKey:    row.DedupeKey,
		Payload:      payload,
		OccurredAt:   row.OccurredAt,
		CreatedAt:    row.CreatedAt,
	}
	if row.DispatchedAt.Valid {
		at := row.DispatchedAt.Time
		ev.DispatchedAt = &at
	}
	return ev, nil
}
