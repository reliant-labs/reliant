package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type triggerStore struct{ q pgdb.Querier }

// NewTriggerStore creates the Postgres trigger store implementation.
func NewTriggerStore(q pgdb.Querier) core.TriggerStore { return &triggerStore{q: q} }

func (s *triggerStore) CreateTrigger(ctx context.Context, t *core.Trigger) error {
	presets, err := triggerMapToJSON(t.Presets)
	if err != nil {
		return fmt.Errorf("marshal trigger presets: %w", err)
	}
	params, err := triggerMapToJSON(t.Params)
	if err != nil {
		return fmt.Errorf("marshal trigger params: %w", err)
	}

	return s.q.CreateTrigger(ctx, pgdb.CreateTriggerParams{
		ID:         t.ID,
		UserID:     t.UserID,
		ProjectID:  t.ProjectID,
		WorktreeID: triggerPtrToNullString(t.WorktreeID),
		Name:       t.Name,
		Kind:       string(t.Kind),
		Enabled:    t.Enabled,
		Workflow:   t.Workflow,
		Presets:    presets,
		Params:     params,
		Message:    t.Message,
		Config:     triggerConfigToJSON(t.Config),
		CreatedAt:  t.CreatedAt,
		UpdatedAt:  t.UpdatedAt,
		DaemonID:   t.DaemonID,
	})
}

func (s *triggerStore) GetTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	row, err := s.q.GetTrigger(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrTriggerNotFound
		}
		return nil, fmt.Errorf("failed to get trigger: %w", err)
	}
	return triggerFromPG(row)
}

func (s *triggerStore) ListTriggers(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error) {
	rows, err := s.q.ListTriggers(ctx, pgdb.ListTriggersParams{
		UserID:    f.UserID,
		ProjectID: triggerPtrToNullString(f.ProjectID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list triggers: %w", err)
	}

	triggers := make([]*core.Trigger, 0, len(rows))
	for _, row := range rows {
		t, err := triggerFromPG(row)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, t)
	}
	return triggers, nil
}

func (s *triggerStore) UpdateTrigger(ctx context.Context, t *core.Trigger) error {
	presets, err := triggerMapToJSON(t.Presets)
	if err != nil {
		return fmt.Errorf("marshal trigger presets: %w", err)
	}
	params, err := triggerMapToJSON(t.Params)
	if err != nil {
		return fmt.Errorf("marshal trigger params: %w", err)
	}

	affected, err := s.q.UpdateTrigger(ctx, pgdb.UpdateTriggerParams{
		ProjectID:  t.ProjectID,
		WorktreeID: triggerPtrToNullString(t.WorktreeID),
		Name:       t.Name,
		Enabled:    t.Enabled,
		Workflow:   t.Workflow,
		Presets:    presets,
		Params:     params,
		Message:    t.Message,
		Config:     triggerConfigToJSON(t.Config),
		UpdatedAt:  t.UpdatedAt,
		DaemonID:   t.DaemonID,
		ID:         t.ID,
	})
	if err != nil {
		return fmt.Errorf("failed to update trigger: %w", err)
	}
	if affected == 0 {
		return core.ErrTriggerNotFound
	}
	return nil
}

func (s *triggerStore) DeleteTrigger(ctx context.Context, id string) error {
	return s.q.DeleteTrigger(ctx, id)
}

func (s *triggerStore) SetTriggerEnabled(ctx context.Context, id string, enabled bool) error {
	affected, err := s.q.SetTriggerEnabled(ctx, pgdb.SetTriggerEnabledParams{Enabled: enabled, ID: id})
	if err != nil {
		return fmt.Errorf("failed to set trigger enabled: %w", err)
	}
	if affected == 0 {
		return core.ErrTriggerNotFound
	}
	return nil
}

// CreateTriggerEvent reports created=false when a row for this (kind,
// dedupe_key) already existed. The INSERT is ON CONFLICT DO NOTHING, so a
// losing caller gets no error and no second row — which is the whole point:
// "a chat starts exactly once" and "a retried scheduled fire launches once"
// are the same database constraint, and the affected-row count is how a
// caller learns which side of it it landed on.
func (s *triggerStore) CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (bool, error) {
	payload, err := triggerMapToJSON(ev.Payload)
	if err != nil {
		return false, fmt.Errorf("marshal trigger event payload: %w", err)
	}

	affected, err := s.q.CreateTriggerEvent(ctx, pgdb.CreateTriggerEventParams{
		ID:            ev.ID,
		TriggerID:     triggerPtrToNullString(ev.TriggerID),
		UserID:        ev.UserID,
		Kind:          string(ev.Kind),
		DedupeKey:     ev.DedupeKey,
		OccurredAt:    ev.OccurredAt,
		Payload:       payload,
		Outcome:       string(ev.Outcome),
		OutcomeDetail: ev.OutcomeDetail,
		ChatID:        triggerPtrToNullString(ev.ChatID),
		CreatedAt:     ev.CreatedAt,
	})
	if err != nil {
		return false, fmt.Errorf("failed to create trigger event: %w", err)
	}
	return affected > 0, nil
}

func (s *triggerStore) GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error) {
	row, err := s.q.GetTriggerEventByDedupe(ctx, pgdb.GetTriggerEventByDedupeParams{
		Kind:      string(kind),
		DedupeKey: dedupeKey,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrTriggerEventNotFound
		}
		return nil, fmt.Errorf("failed to get trigger event by dedupe key: %w", err)
	}
	return triggerEventFromPG(row)
}

func (s *triggerStore) GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error) {
	row, err := s.q.GetTriggerEventByChatID(ctx, triggerPtrToNullString(&chatID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrTriggerEventNotFound
		}
		return nil, fmt.Errorf("failed to get trigger event by chat id: %w", err)
	}
	return triggerEventFromPG(row)
}

func (s *triggerStore) UpdateTriggerEventOutcome(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string, chatID *string) error {
	affected, err := s.q.UpdateTriggerEventOutcome(ctx, pgdb.UpdateTriggerEventOutcomeParams{
		Outcome:       string(outcome),
		OutcomeDetail: detail,
		ChatID:        triggerPtrToNullString(chatID),
		ID:            id,
	})
	if err != nil {
		return fmt.Errorf("failed to update trigger event outcome: %w", err)
	}
	if affected == 0 {
		return core.ErrTriggerEventNotFound
	}
	return nil
}

func (s *triggerStore) ListTriggerEvents(ctx context.Context, triggerID string, limit int) ([]*core.TriggerEvent, error) {
	if limit <= 0 {
		limit = defaultTriggerEventLimit
	}

	rows, err := s.q.ListTriggerEvents(ctx, pgdb.ListTriggerEventsParams{
		TriggerID: triggerPtrToNullString(&triggerID),
		Limit:     int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list trigger events: %w", err)
	}

	events := make([]*core.TriggerEvent, 0, len(rows))
	for _, row := range rows {
		ev, err := triggerEventFromPG(row)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, nil
}

// defaultTriggerEventLimit bounds an unspecified limit. An unbounded history
// query is never what a caller wants here: the firing log grows once per
// scheduled tick forever.
const defaultTriggerEventLimit = 50

func (s *triggerStore) GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error) {
	var outcomeArg sql.NullString
	if outcome != nil {
		outcomeArg = sql.NullString{String: string(*outcome), Valid: true}
	}

	row, err := s.q.GetLatestTriggerEvent(ctx, pgdb.GetLatestTriggerEventParams{
		TriggerID: triggerID,
		Outcome:   outcomeArg,
	})
	if err != nil {
		// "no events yet" is the ordinary state of a trigger that has never
		// fired, and of the overlap check's first run. Not an error.
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get latest trigger event: %w", err)
	}
	return triggerEventFromPG(row)
}

func triggerFromPG(row pgdb.Trigger) (*core.Trigger, error) {
	presets := map[string]string{}
	if err := triggerJSONToMap(row.Presets, &presets); err != nil {
		return nil, fmt.Errorf("unmarshal trigger presets (trigger %s): %w", row.ID, err)
	}
	params := map[string]any{}
	if err := triggerJSONToMap(row.Params, &params); err != nil {
		return nil, fmt.Errorf("unmarshal trigger params (trigger %s): %w", row.ID, err)
	}

	config := json.RawMessage(nil)
	if len(row.Config) > 0 {
		config = json.RawMessage(append([]byte(nil), row.Config...))
	}

	return &core.Trigger{
		ID:         row.ID,
		UserID:     row.UserID,
		ProjectID:  row.ProjectID,
		WorktreeID: triggerNullStringToPtr(row.WorktreeID),
		Name:       row.Name,
		Kind:       core.TriggerKind(row.Kind),
		Enabled:    row.Enabled,
		Workflow:   row.Workflow,
		Presets:    presets,
		Params:     params,
		Message:    row.Message,
		Config:     config,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
		DaemonID:   row.DaemonID,
	}, nil
}

func triggerEventFromPG(row pgdb.TriggerEvent) (*core.TriggerEvent, error) {
	payload := map[string]any{}
	if err := triggerJSONToMap(row.Payload, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal trigger event payload (event %s): %w", row.ID, err)
	}

	return &core.TriggerEvent{
		ID:            row.ID,
		TriggerID:     triggerNullStringToPtr(row.TriggerID),
		UserID:        row.UserID,
		Kind:          core.TriggerEventKind(row.Kind),
		DedupeKey:     row.DedupeKey,
		OccurredAt:    row.OccurredAt,
		Payload:       payload,
		Outcome:       core.TriggerEventOutcome(row.Outcome),
		OutcomeDetail: row.OutcomeDetail,
		ChatID:        triggerNullStringToPtr(row.ChatID),
		CreatedAt:     row.CreatedAt,
	}, nil
}

// triggerMapToJSON always produces a jsonb OBJECT, never a JSON null: a nil Go
// map marshals to `null`, which reads back as a nil map and would force every
// consumer to nil-check a field the schema declares NOT NULL with a '{}'
// default.
func triggerMapToJSON[V any](m map[string]V) (json.RawMessage, error) {
	if m == nil {
		return json.RawMessage(`{}`), nil
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// triggerConfigToJSON passes a stored RawMessage through, substituting an
// empty object for an absent one so the NOT NULL column is always satisfied.
func triggerConfigToJSON(config json.RawMessage) json.RawMessage {
	if len(config) == 0 {
		return json.RawMessage(`{}`)
	}
	return config
}

func triggerJSONToMap(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func triggerPtrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func triggerNullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		value := ns.String
		return &value
	}
	return nil
}
