// Copyright (c) 2025 Reliant Labs
package core

import (
	"encoding/json"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// UserUpdate represents an update in the user_updates table.
// This is for workspace-level updates pushed via the global WebSocket.
type UserUpdate struct {
	ID             string                   `json:"id"`
	UserID         string                   `json:"user_id"`
	SequenceNumber int64                    `json:"sequence_number"`
	ProjectID      *string                  `json:"project_id,omitempty"`
	WorktreeID     *string                  `json:"worktree_id,omitempty"`
	ChatID         *string                  `json:"chat_id,omitempty"`
	UpdateType     reliantv1.UserUpdateType `json:"update_type"`
	EntityType     reliantv1.EntityType     `json:"entity_type"`
	EntityID       string                   `json:"entity_id"`
	Data           json.RawMessage          `json:"data"`
	CreatedAt      time.Time                `json:"created_at"`
}
