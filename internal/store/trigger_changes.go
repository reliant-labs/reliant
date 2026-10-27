// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// RecordTriggerChange is called in the SAME transaction as the row mutation.
// Only the bounded allowlisted projection is persisted: arbitrary params,
// prompts, source expressions, and graph literals are never audit data.
func RecordTriggerChange(ctx context.Context, db orm.Context, op string, before, after *core.Trigger) error {
	var old any
	if before != nil {
		data, err := automationpolicy.SafeJSON(before)
		if err != nil {
			return err
		}
		old = string(data)
	}
	data, err := automationpolicy.SafeJSON(after)
	if err != nil {
		return err
	}
	actor := automationpolicy.ActorFrom(ctx)
	_, err = db.Bun().NewRaw(`INSERT INTO trigger_changes(id,trigger_id,user_id,chat_id,run_id,tool_call_id,operation,before_state,after_state,created_at) VALUES (?,?,?,?,?,?,?,?::jsonb,?::jsonb,?)`, uuid.NewString(), after.ID, after.UserID, actor.ChatID, actor.RunID, actor.ToolCallID, op, old, string(data), time.Now().UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("record trigger configuration change: %w", err)
	}
	return nil
}

// TriggerChanges reads a bounded newest-first configuration history, owner scoped.
func TriggerChanges(ctx context.Context, db orm.Context, userID, triggerID string) ([]core.TriggerChange, error) {
	var out []core.TriggerChange
	err := db.Bun().NewRaw(`SELECT id,user_id,chat_id,run_id,tool_call_id,operation,COALESCE(before_state::text,'') AS before_state,after_state::text AS after_state,created_at FROM trigger_changes WHERE user_id=? AND trigger_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, userID, triggerID).Scan(ctx, &out)
	return out, err
}
