// Copyright (c) 2025 Reliant Labs
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

type integrationWriteStore struct{ q pgdb.Querier }

// NewIntegrationWriteStore creates the Postgres write ledger.
func NewIntegrationWriteStore(q pgdb.Querier) core.IntegrationWriteStore {
	return &integrationWriteStore{q: q}
}

func (s *integrationWriteStore) CreateIntegrationWrite(ctx context.Context, w *core.IntegrationWrite) error {
	correlation, err := json.Marshal(nonNilMap(w.Correlation))
	if err != nil {
		return fmt.Errorf("marshal integration write correlation: %w", err)
	}
	actor := w.Actor
	if actor == "" {
		actor = core.IntegrationActorOwner
	}
	status := w.Status
	if status == "" {
		status = core.IntegrationWriteIntent
	}
	var completed sql.NullTime
	if w.CompletedAt != nil {
		completed = sql.NullTime{Time: w.CompletedAt.UTC(), Valid: true}
	}
	if err := s.q.CreateIntegrationWrite(ctx, pgdb.CreateIntegrationWriteParams{
		ID: w.ID, UserID: w.UserID, ChatID: w.ChatID, Workflow: w.WorkflowName,
		TriggerID: w.TriggerID, TriggerName: w.TriggerName, NodeID: w.NodeID, ToolCallID: w.ToolCallID,
		Integration: w.Integration, Action: w.Action, ConnectionID: w.ConnectionID, Actor: actor,
		Scope: w.Scope, Target: w.Target, Correlation: correlation,
		ObjectID: w.ObjectID, ObjectUrl: w.ObjectURL, Status: string(status),
		Ambiguous: w.Ambiguous, Error: w.Error, CreatedAt: w.CreatedAt.UTC(), CompletedAt: completed,
	}); err != nil {
		return fmt.Errorf("create integration write: %w", err)
	}
	return nil
}

func (s *integrationWriteStore) CompleteIntegrationWrite(ctx context.Context, id string, res core.IntegrationWriteResult) (bool, error) {
	at := res.At
	if at.IsZero() {
		at = time.Now()
	}
	n, err := s.q.CompleteIntegrationWrite(ctx, pgdb.CompleteIntegrationWriteParams{
		ID: id, Status: string(res.Status), Target: res.Target, ObjectID: res.ObjectID, ObjectUrl: res.ObjectURL,
		ConnectionID: res.ConnectionID, Ambiguous: res.Ambiguous, Error: res.Error, CompletedAt: at.UTC(),
	})
	if err != nil {
		return false, fmt.Errorf("complete integration write: %w", err)
	}
	return n > 0, nil
}

func (s *integrationWriteStore) GetIntegrationWriteByObject(ctx context.Context, integration, scope, objectKind, objectID string) (*core.IntegrationWrite, error) {
	row, err := s.q.GetIntegrationWriteByObject(ctx, pgdb.GetIntegrationWriteByObjectParams{
		Integration: integration, Scope: scope, ObjectKind: objectKind, ObjectID: objectID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrIntegrationWriteNotFound
		}
		return nil, fmt.Errorf("get integration write by object: %w", err)
	}
	return integrationWriteFromPG(row)
}

func (s *integrationWriteStore) ListRecentIntegrationWrites(ctx context.Context, integration, scope string, since time.Time, limit int) ([]*core.IntegrationWrite, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.q.ListRecentIntegrationWrites(ctx, pgdb.ListRecentIntegrationWritesParams{
		Integration: integration, Scope: scope, Since: since.UTC(), RowLimit: int32(limit), //nolint:gosec // bounded by the caller
	})
	if err != nil {
		return nil, fmt.Errorf("list recent integration writes: %w", err)
	}
	out := make([]*core.IntegrationWrite, 0, len(rows))
	for _, row := range rows {
		w, err := integrationWriteFromPG(row)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

func (s *integrationWriteStore) PruneIntegrationWrites(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.q.PruneIntegrationWrites(ctx, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune integration writes: %w", err)
	}
	return n, nil
}

func integrationWriteFromPG(row pgdb.IntegrationWrite) (*core.IntegrationWrite, error) {
	correlation := map[string]any{}
	if len(row.Correlation) > 0 {
		if err := json.Unmarshal(row.Correlation, &correlation); err != nil {
			return nil, fmt.Errorf("unmarshal integration write correlation (write %s): %w", row.ID, err)
		}
	}
	w := &core.IntegrationWrite{
		ID: row.ID, UserID: row.UserID, ChatID: row.ChatID, WorkflowName: row.Workflow,
		TriggerID: row.TriggerID, TriggerName: row.TriggerName, NodeID: row.NodeID, ToolCallID: row.ToolCallID,
		Integration: row.Integration, Action: row.Action, ConnectionID: row.ConnectionID, Actor: row.Actor,
		Scope: row.Scope, Target: row.Target, Correlation: correlation,
		ObjectID: row.ObjectID, ObjectURL: row.ObjectUrl, Status: core.IntegrationWriteStatus(row.Status),
		Ambiguous: row.Ambiguous, Error: row.Error, CreatedAt: row.CreatedAt,
	}
	if row.CompletedAt.Valid {
		t := row.CompletedAt.Time
		w.CompletedAt = &t
	}
	return w, nil
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
