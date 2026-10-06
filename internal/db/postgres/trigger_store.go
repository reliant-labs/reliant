package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
		DaemonID:   triggerDaemonToNull(t.DaemonID),
		NoMachine:  t.NoMachine,

		NotifyOnComplete: t.NotifyOnComplete,
		Filter:           t.Filter,
		ConnectionID:     triggerPtrToNullString(t.ConnectionID),
		WorkflowTrigger:  triggerPtrToNullString(t.WorkflowTrigger),
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
	return triggerFromPGNamed(row.Trigger, row.ProjectName, row.DaemonName)
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
		t, err := triggerFromPGNamed(row.Trigger, row.ProjectName, row.DaemonName)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, t)
	}
	return triggers, nil
}

func (s *triggerStore) ListAllTriggers(ctx context.Context) ([]*core.Trigger, error) {
	rows, err := s.q.ListAllTriggers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list all triggers: %w", err)
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

func (s *triggerStore) LockTrigger(ctx context.Context, id string) error {
	if _, err := s.q.LockTrigger(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrTriggerNotFound
		}
		return fmt.Errorf("failed to lock trigger: %w", err)
	}
	return nil
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
		ProjectID:        t.ProjectID,
		WorktreeID:       triggerPtrToNullString(t.WorktreeID),
		Name:             t.Name,
		Enabled:          t.Enabled,
		Workflow:         t.Workflow,
		Presets:          presets,
		Params:           params,
		Message:          t.Message,
		Config:           triggerConfigToJSON(t.Config),
		UpdatedAt:        t.UpdatedAt,
		DaemonID:         triggerDaemonToNull(t.DaemonID),
		NotifyOnComplete: t.NotifyOnComplete,
		Filter:           t.Filter,
		ConnectionID:     triggerPtrToNullString(t.ConnectionID),
		WorkflowTrigger:  triggerPtrToNullString(t.WorkflowTrigger),
		NoMachine:        t.NoMachine,
		ID:               t.ID,
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

func (s *triggerStore) GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error) {
	row, err := s.q.GetTriggerEventByChat(ctx, pgdb.GetTriggerEventByChatParams{
		Kind:   string(kind),
		ChatID: triggerPtrToNullString(&chatID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrTriggerEventNotFound
		}
		return nil, fmt.Errorf("failed to get trigger event by chat: %w", err)
	}
	return triggerEventFromPG(row)
}

func (s *triggerStore) CountLiveLaunchedRuns(ctx context.Context, userID string, kind core.TriggerEventKind) (int, error) {
	count, err := s.q.CountLiveLaunchedRuns(ctx, pgdb.CountLiveLaunchedRunsParams{
		UserID: userID,
		Kind:   string(kind),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to count live launched runs: %w", err)
	}
	return int(count), nil
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

func (s *triggerStore) UpdateTriggerEventPayload(ctx context.Context, id string, payload map[string]any) error {
	encoded, err := triggerMapToJSON(payload)
	if err != nil {
		return fmt.Errorf("marshal trigger event payload: %w", err)
	}
	affected, err := s.q.UpdateTriggerEventPayload(ctx, pgdb.UpdateTriggerEventPayloadParams{Payload: encoded, ID: id})
	if err != nil {
		return fmt.Errorf("failed to update trigger event payload: %w", err)
	}
	if affected == 0 {
		return core.ErrTriggerEventNotFound
	}
	return nil
}

// ListTriggerEvents returns one page. It reads one row beyond the limit to
// learn whether another page exists without a count.
func (s *triggerStore) ListTriggerEvents(ctx context.Context, f core.TriggerEventFilters) ([]*core.TriggerEventWithRun, bool, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultTriggerEventLimit
	}
	outcomes := make([]string, len(f.Outcomes))
	for i, o := range f.Outcomes {
		outcomes[i] = string(o)
	}
	params := pgdb.ListTriggerEventsParams{
		TriggerID: f.TriggerID,
		UserID:    f.UserID,
		Outcomes:  outcomes,
		RowLimit:  int32(limit + 1),
	}
	if f.After != nil {
		params.CursorOccurredAt = sql.NullTime{Time: f.After.OccurredAt, Valid: true}
		params.CursorID = sql.NullString{String: f.After.ID, Valid: true}
	}

	rows, err := s.q.ListTriggerEvents(ctx, params)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list trigger events: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	events := make([]*core.TriggerEventWithRun, 0, len(rows))
	for _, row := range rows {
		ev, err := triggerEventFromPG(row.TriggerEvent)
		if err != nil {
			return nil, false, err
		}
		events = append(events, &core.TriggerEventWithRun{
			Event: ev,
			Run:   triggerRunFromPG(row.RunChatID, row.RunTitle, row.RunRootState, row.RunRootStopReason, row.RunDisplayState),
		})
	}
	return events, hasMore, nil
}

func (s *triggerStore) RecentTriggerFirings(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	out := make(map[string][]*core.TriggerEventWithRun, len(triggerIDs))
	if len(triggerIDs) == 0 || perTrigger <= 0 {
		return out, nil
	}
	rows, err := s.q.ListRecentTriggerFirings(ctx, pgdb.ListRecentTriggerFiringsParams{
		UserID:     userID,
		TriggerIds: triggerIDs,
		PerTrigger: int32(perTrigger),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list recent trigger firings: %w", err)
	}
	for _, row := range rows {
		ev, err := triggerEventFromPG(row.TriggerEvent)
		if err != nil {
			return nil, err
		}
		if ev.TriggerID == nil {
			continue
		}
		out[*ev.TriggerID] = append(out[*ev.TriggerID], &core.TriggerEventWithRun{
			Event: ev,
			Run:   triggerRunFromPG(row.RunChatID, row.RunTitle, row.RunRootState, row.RunRootStopReason, row.RunDisplayState),
		})
	}
	return out, nil
}

func (s *triggerStore) FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	out := make(map[string][]*core.TriggerEventWithRun, len(triggerIDs))
	if len(triggerIDs) == 0 || perTrigger <= 0 {
		return out, nil
	}
	rows, err := s.q.ListFiringsSinceLastSuccess(ctx, pgdb.ListFiringsSinceLastSuccessParams{
		UserID:     userID,
		TriggerIds: triggerIDs,
		PerTrigger: int32(perTrigger),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list firings since last success: %w", err)
	}
	for _, row := range rows {
		ev, err := triggerEventFromPG(row.TriggerEvent)
		if err != nil {
			return nil, err
		}
		if ev.TriggerID == nil {
			continue
		}
		out[*ev.TriggerID] = append(out[*ev.TriggerID], &core.TriggerEventWithRun{
			Event: ev,
			Run:   triggerRunFromPG(row.RunChatID, row.RunTitle, row.RunRootState, row.RunRootStopReason, row.RunDisplayState),
		})
	}
	return out, nil
}

// triggerRunFromPG is nil unless the firing joined a chat that still exists.
func triggerRunFromPG(chatID, title sql.NullString, rootState, rootStop sql.NullInt32, display int32) *core.TriggerEventRun {
	if !chatID.Valid {
		return nil
	}
	return &core.TriggerEventRun{
		ChatID:       chatID.String,
		Title:        title.String,
		DisplayState: core.RunDisplayState(display),
		RootStatus:   chatRootStatus(rootState, rootStop),
	}
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

func triggerFromPGNamed(row pgdb.Trigger, projectName, daemonName string) (*core.Trigger, error) {
	t, err := triggerFromPG(row)
	if err != nil {
		return nil, err
	}
	t.ProjectName = projectName
	t.DaemonName = daemonName
	return t, nil
}

// TriggerFromRow maps a triggers row read by a query outside this store (the
// access-gated routing query joins triggers to integration_event_access).
func TriggerFromRow(row pgdb.Trigger) (*core.Trigger, error) { return triggerFromPG(row) }

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
		ID:               row.ID,
		UserID:           row.UserID,
		ProjectID:        row.ProjectID,
		WorktreeID:       triggerNullStringToPtr(row.WorktreeID),
		Name:             row.Name,
		Kind:             core.TriggerKind(row.Kind),
		Enabled:          row.Enabled,
		Workflow:         row.Workflow,
		Presets:          presets,
		Params:           params,
		Message:          row.Message,
		Config:           config,
		Filter:           row.Filter,
		ConnectionID:     triggerNullStringToPtr(row.ConnectionID),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
		DaemonID:         row.DaemonID.String,
		NoMachine:        row.NoMachine,
		NotifyOnComplete: row.NotifyOnComplete,
		WorkflowTrigger:  triggerNullStringToPtr(row.WorkflowTrigger),
	}, nil
}

func (s *triggerStore) SetTriggerProjection(ctx context.Context, id string, p core.TriggerProjection) error {
	affected, err := s.q.SetTriggerProjection(ctx, pgdb.SetTriggerProjectionParams{
		Config: triggerConfigToJSON(p.Config),
		Filter: p.Filter,
		ID:     id,
	})
	if err != nil {
		return fmt.Errorf("failed to set trigger projection: %w", err)
	}
	if affected == 0 {
		return core.ErrTriggerNotFound
	}
	return nil
}

func (s *triggerStore) ListWorkflowTriggerActivations(ctx context.Context, userID, workflow string) ([]*core.Trigger, error) {
	rows, err := s.q.ListWorkflowTriggerActivations(ctx, pgdb.ListWorkflowTriggerActivationsParams{UserID: userID, Workflow: workflow})
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow trigger activations: %w", err)
	}
	return triggersFromPG(rows)
}

func (s *triggerStore) ListAllWorkflowTriggerActivations(ctx context.Context) ([]*core.Trigger, error) {
	rows, err := s.q.ListAllWorkflowTriggerActivations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow trigger activations: %w", err)
	}
	return triggersFromPG(rows)
}

func triggersFromPG(rows []pgdb.Trigger) ([]*core.Trigger, error) {
	out := make([]*core.Trigger, 0, len(rows))
	for _, row := range rows {
		t, err := triggerFromPG(row)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// triggerDaemonToNull stores a no-machine trigger's empty daemon as NULL,
// which is what triggers_machine_choice_check pairs with no_machine.
func triggerDaemonToNull(daemonID string) sql.NullString {
	return sql.NullString{String: daemonID, Valid: daemonID != ""}
}

func (s *triggerStore) SetTriggerWebhookTokenHash(ctx context.Context, id string, hash []byte) error {
	affected, err := s.q.SetTriggerWebhookTokenHash(ctx, pgdb.SetTriggerWebhookTokenHashParams{WebhookTokenHash: hash, ID: id})
	if err != nil {
		return fmt.Errorf("failed to set webhook token: %w", err)
	}
	if affected == 0 {
		return core.ErrTriggerNotFound
	}
	return nil
}

func (s *triggerStore) SetTriggerWebhookSecret(ctx context.Context, id string, sealed []byte) error {
	affected, err := s.q.SetTriggerWebhookSecret(ctx, pgdb.SetTriggerWebhookSecretParams{WebhookSecretSealed: sealed, ID: id})
	if err != nil {
		return fmt.Errorf("failed to set webhook secret: %w", err)
	}
	if affected == 0 {
		return core.ErrTriggerNotFound
	}
	return nil
}

func (s *triggerStore) GetTriggerWebhookCredentials(ctx context.Context, id string) (*core.TriggerWebhookCredentials, error) {
	row, err := s.q.GetTriggerWebhookCredentials(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrTriggerNotFound
		}
		return nil, fmt.Errorf("failed to get webhook credentials: %w", err)
	}
	return &core.TriggerWebhookCredentials{TokenHash: row.WebhookTokenHash, SecretSealed: row.WebhookSecretSealed}, nil
}

func (s *triggerStore) ListIntegrationTriggers(ctx context.Context, integration string) ([]*core.IntegrationTriggerRoute, error) {
	rows, err := s.q.ListIntegrationTriggers(ctx, integration)
	if err != nil {
		return nil, fmt.Errorf("failed to list integration triggers: %w", err)
	}
	out := make([]*core.IntegrationTriggerRoute, 0, len(rows))
	for _, row := range rows {
		t, err := triggerFromPG(row.Trigger)
		if err != nil {
			return nil, err
		}
		out = append(out, &core.IntegrationTriggerRoute{
			Trigger:           t,
			ConnectionAccount: row.ConnectionAccount.String,
			ConnectionStatus:  row.ConnectionStatus,
		})
	}
	return out, nil
}

func (s *triggerStore) ListStalePendingTriggerEvents(ctx context.Context, olderThan time.Time, limit int) ([]*core.TriggerEvent, error) {
	if limit <= 0 {
		limit = defaultTriggerEventLimit
	}
	rows, err := s.q.ListStalePendingTriggerEvents(ctx, pgdb.ListStalePendingTriggerEventsParams{
		OlderThan: olderThan,
		RowLimit:  int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pending trigger events: %w", err)
	}
	out := make([]*core.TriggerEvent, 0, len(rows))
	for _, row := range rows {
		ev, err := triggerEventFromPG(row)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func (s *triggerStore) ClaimPendingTriggerEvent(ctx context.Context, id string, payload map[string]any) (bool, error) {
	encoded, err := triggerMapToJSON(payload)
	if err != nil {
		return false, fmt.Errorf("marshal trigger event payload: %w", err)
	}
	affected, err := s.q.ClaimPendingTriggerEvent(ctx, pgdb.ClaimPendingTriggerEventParams{Payload: encoded, ID: id})
	if err != nil {
		return false, fmt.Errorf("failed to claim pending trigger event: %w", err)
	}
	return affected == 1, nil
}

func (s *triggerStore) SettlePendingTriggerEvent(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string) (bool, error) {
	affected, err := s.q.SettlePendingTriggerEvent(ctx, pgdb.SettlePendingTriggerEventParams{
		Outcome: string(outcome), OutcomeDetail: detail, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("failed to settle pending trigger event: %w", err)
	}
	return affected == 1, nil
}

func (s *triggerStore) GetTriggerRegistration(ctx context.Context, triggerID string) (*core.TriggerRegistration, error) {
	row, err := s.q.GetTriggerRegistration(ctx, triggerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrTriggerRegistrationNotFound
		}
		return nil, fmt.Errorf("failed to get trigger registration: %w", err)
	}
	return triggerRegistrationFromPG(row), nil
}

func (s *triggerStore) ListTriggerRegistrations(ctx context.Context, userID string, triggerIDs []string) (map[string]*core.TriggerRegistration, error) {
	out := map[string]*core.TriggerRegistration{}
	if len(triggerIDs) == 0 {
		return out, nil
	}
	rows, err := s.q.ListTriggerRegistrations(ctx, pgdb.ListTriggerRegistrationsParams{UserID: userID, TriggerIds: triggerIDs})
	if err != nil {
		return nil, fmt.Errorf("failed to list trigger registrations: %w", err)
	}
	for _, row := range rows {
		out[row.TriggerID] = triggerRegistrationFromPG(row)
	}
	return out, nil
}

func triggerRegistrationFromPG(row pgdb.TriggerRegistration) *core.TriggerRegistration {
	reg := &core.TriggerRegistration{
		TriggerID:      row.TriggerID,
		Provider:       row.Provider,
		RegistrationID: row.RegistrationID,
		Cursor:         row.Cursor,
		Status:         row.Status,
		StatusDetail:   row.StatusDetail,
		StatusSince:    row.StatusSince,
		LastGapDetail:  row.LastGapDetail,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if row.LastPolledAt.Valid {
		at := row.LastPolledAt.Time
		reg.LastPolledAt = &at
	}
	if row.LastGapAt.Valid {
		at := row.LastGapAt.Time
		reg.LastGapAt = &at
	}
	return reg
}

func (s *triggerStore) UpsertTriggerRegistration(ctx context.Context, reg *core.TriggerRegistration) error {
	status := reg.Status
	if status == "" {
		status = core.TriggerRegistrationActive
	}
	var polled, gap sql.NullTime
	if reg.LastPolledAt != nil {
		polled = sql.NullTime{Time: *reg.LastPolledAt, Valid: true}
	}
	if reg.LastGapAt != nil {
		gap = sql.NullTime{Time: *reg.LastGapAt, Valid: true}
	}
	return s.q.UpsertTriggerRegistration(ctx, pgdb.UpsertTriggerRegistrationParams{
		TriggerID:      reg.TriggerID,
		Provider:       reg.Provider,
		RegistrationID: reg.RegistrationID,
		Cursor:         reg.Cursor,
		LastPolledAt:   polled,
		Status:         status,
		StatusDetail:   reg.StatusDetail,
		LastGapAt:      gap,
		LastGapDetail:  reg.LastGapDetail,
	})
}

func (s *triggerStore) DeleteTriggerRegistration(ctx context.Context, triggerID string) error {
	return s.q.DeleteTriggerRegistration(ctx, triggerID)
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
		RunStatus:     row.RunStatus.String,
	}, nil
}

func (s *triggerStore) SetLaunchEventRunStatus(ctx context.Context, chatID, status string) error {
	if _, err := s.q.SetLaunchEventRunStatus(ctx, pgdb.SetLaunchEventRunStatusParams{RunStatus: status, ChatID: chatID}); err != nil {
		return fmt.Errorf("failed to record launch event run status: %w", err)
	}
	return nil
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
