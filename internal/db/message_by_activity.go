// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
)

// GetMessageByActivityID retrieves a message by chat_id and activity_id
func (r *Repo) GetMessageByActivityID(ctx context.Context, chatID, activityID string) (*Message, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chatID cannot be empty")
	}
	if activityID == "" {
		return nil, fmt.Errorf("activityID cannot be empty")
	}

	tx := r.DB.DB(ctx)
	query := `
		SELECT id, chat_id, ordinal, context_window_id, role,
		       model, agent, token_count, cost,
		       workflow_id, run_id, node_id, node_path, activity_id, created_at, updated_at
		FROM messages
		WHERE chat_id = ? AND activity_id = ?
		LIMIT 1
	`
	query = r.bindQuery(query)
	var msg Message
	err := tx.QueryRowContext(ctx, query, chatID, activityID).Scan(
		&msg.ID, &msg.ChatID, &msg.Ordinal, &msg.ContextWindowID, &msg.Role,
		&msg.Model, &msg.Agent, &msg.TokenCount, &msg.Cost,
		&msg.WorkflowID, &msg.RunID, &msg.NodeID, &msg.NodePath, &msg.ActivityID, &msg.CreatedAt, &msg.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil // Not found - this is OK for idempotency checks
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get message by activity ID: %w", err)
	}

	return &msg, nil
}

// GetMessageByWorkflowAndActivityID retrieves a message by chat_id, workflow_id, and activity_id.
// This is the preferred idempotency check when workflow_id is available, as it prevents
// collisions across different child workflows that may have the same activity IDs.
func (r *Repo) GetMessageByWorkflowAndActivityID(ctx context.Context, chatID, workflowID, activityID string) (*Message, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chatID cannot be empty")
	}
	if workflowID == "" {
		return nil, fmt.Errorf("workflowID cannot be empty")
	}
	if activityID == "" {
		return nil, fmt.Errorf("activityID cannot be empty")
	}

	tx := r.DB.DB(ctx)
	query := `
		SELECT id, chat_id, ordinal, context_window_id, role,
		       model, agent, token_count, cost,
		       workflow_id, run_id, node_id, node_path, activity_id, created_at, updated_at
		FROM messages
		WHERE chat_id = ? AND workflow_id = ? AND activity_id = ?
		LIMIT 1
	`
	query = r.bindQuery(query)
	var msg Message
	err := tx.QueryRowContext(ctx, query, chatID, workflowID, activityID).Scan(
		&msg.ID, &msg.ChatID, &msg.Ordinal, &msg.ContextWindowID, &msg.Role,
		&msg.Model, &msg.Agent, &msg.TokenCount, &msg.Cost,
		&msg.WorkflowID, &msg.RunID, &msg.NodeID, &msg.NodePath, &msg.ActivityID, &msg.CreatedAt, &msg.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil // Not found - this is OK for idempotency checks
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get message by workflow and activity ID: %w", err)
	}

	return &msg, nil
}
