// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func nonNil(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// TriggerChanges reads a bounded newest-first configuration history, owner scoped.
func TriggerChanges(ctx context.Context, db orm.Context, userID, triggerID string) ([]core.TriggerChange, error) {
	var rows []struct {
		ID               string
		UserID           string
		ChatID           string
		RunID            string
		ToolCallID       string
		Operation        string
		BeforeState      string
		AfterState       string
		Approval         string
		CapabilityChange string
		CreatedAt        time.Time
	}
	err := db.Bun().NewRaw(`SELECT id,user_id,chat_id,run_id,tool_call_id,operation,COALESCE(before_state::text,'') AS before_state,after_state::text AS after_state,approval,COALESCE(capability_change::text,'') AS capability_change,created_at FROM trigger_changes WHERE user_id=? AND trigger_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, userID, triggerID).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	out := make([]core.TriggerChange, 0, len(rows))
	for _, row := range rows {
		change := core.TriggerChange{ID: row.ID, UserID: row.UserID, ChatID: row.ChatID, RunID: row.RunID, ToolCallID: row.ToolCallID, Operation: row.Operation, BeforeState: row.BeforeState, AfterState: row.AfterState, Approval: row.Approval, CreatedAt: row.CreatedAt}
		if row.CapabilityChange != "" {
			var moved core.CapabilityChange
			if json.Unmarshal([]byte(row.CapabilityChange), &moved) == nil {
				change.CapabilitiesAdded, change.CapabilitiesRemoved = moved.Added, moved.Removed
			}
		}
		out = append(out, change)
	}
	return out, nil
}
