// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
)

// FindMessage retrieves a message by id, returning (nil, nil) when no such row
// exists.
//
// GetMessage treats a miss as an error, which is right for callers that are
// dereferencing a known id. This variant exists for the idempotency checks in
// SaveMessage, which ask "is this id already taken?" and must be able to hear
// "no" without unwrapping an error string. It mirrors GetMessageByActivityID's
// contract deliberately, so both idempotency lookups read the same way.
func (r *Repo) FindMessage(ctx context.Context, id string) (*Message, error) {
	if id == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}

	tx := r.DB.DB(ctx)
	query := `
		SELECT id, chat_id, ordinal, context_window_id, role,
		       model, agent, token_count, cost,
		       workflow_id, run_id, node_id, node_path, activity_id, created_at, updated_at
		FROM messages
		WHERE id = ?
		LIMIT 1
	`
	query = r.bindQuery(query)
	var msg Message
	err := tx.QueryRowContext(ctx, query, id).Scan(
		&msg.ID, &msg.ChatID, &msg.Ordinal, &msg.ContextWindowID, &msg.Role,
		&msg.Model, &msg.Agent, &msg.TokenCount, &msg.Cost,
		&msg.WorkflowID, &msg.RunID, &msg.NodeID, &msg.NodePath, &msg.ActivityID, &msg.CreatedAt, &msg.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil // Not found - this is OK for idempotency checks
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find message by ID: %w", err)
	}

	return &msg, nil
}
