// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/store"
)

// GetMessageByActivityID retrieves a message by chat_id and activity_id, or
// (nil, nil) when none exists.
func (r *Repo) GetMessageByActivityID(ctx context.Context, chatID, activityID string) (*Message, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chatID cannot be empty")
	}
	if activityID == "" {
		return nil, fmt.Errorf("activityID cannot be empty")
	}
	msg, err := store.FindMessageByActivity(ctx, r.ORM(), chatID, "", activityID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message by activity ID: %w", err)
	}
	return msg, nil
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
	msg, err := store.FindMessageByActivity(ctx, r.ORM(), chatID, workflowID, activityID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message by workflow and activity ID: %w", err)
	}
	return msg, nil
}
