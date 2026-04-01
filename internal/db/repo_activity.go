// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// GetChatActivity queries the chats_with_activity view to get the computed
// activity for a chat. Returns 0 (IDLE) if no activity exists.
func (r *Repo) GetChatActivity(ctx context.Context, chatID string) (int, error) {
	query := `SELECT COALESCE(activity, 0) FROM chats_with_activity WHERE id = ?`
	query = r.bindQuery(query)

	var activity int
	if err := r.DB.DB(ctx).QueryRowContext(ctx, query, chatID).Scan(&activity); err != nil {
		return 0, fmt.Errorf("failed to get chat activity: %w", err)
	}
	return activity, nil
}

// emitChatActivityIfChanged queries the current computed activity for a chat
// and emits a chat_activity_changed event. Dedup is handled on the frontend
// (activityStore skips no-op updates), so we always emit here.
func (r *Repo) emitChatActivityIfChanged(ctx context.Context, chatID string) error {
	activity, err := r.GetChatActivity(ctx, chatID)
	if err != nil {
		return err
	}
	return r.emitChatActivityChanged(ctx, chatID, activity)
}

// emitChatActivityChanged builds a UserUpdate with UpdateType 6
// (CHAT_ACTIVITY_CHANGED) and persists it via CreateUserUpdate.
func (r *Repo) emitChatActivityChanged(ctx context.Context, chatID string, activity int) error {
	chat, err := r.GetChat(ctx, chatID)
	if err != nil {
		return fmt.Errorf("failed to get chat for activity event: %w", err)
	}

	updateData := map[string]interface{}{
		"chat_id":   chatID,
		"activity":  activity,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal activity event data: %w", err)
	}

	userUpdate := &UserUpdate{
		UserID:     chat.UserID,
		ProjectID:  &chat.ProjectID,
		WorktreeID: chat.WorktreeID,
		ChatID:     &chatID,
		UpdateType: UserUpdateChatActivityChanged,
		EntityType: EntityTypeChat,
		EntityID:   chatID,
		Data:       updateDataJSON,
	}

	if err := r.CreateUserUpdate(ctx, userUpdate); err != nil {
		return fmt.Errorf("failed to create activity event: %w", err)
	}

	logging.Info("[ActivityEvent] Emitted chat_activity_changed",
		"chatID", chatID,
		"activity", activity,
	)

	return nil
}
