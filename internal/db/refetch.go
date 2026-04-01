package db

import (
	"context"
	"encoding/json"
	"fmt"
)

// RefetchType identifies what data the frontend should re-fetch.
type RefetchType string

const (
	RefetchWorktreeChanges    RefetchType = "worktree_changes"
	RefetchWorkflowExecutions RefetchType = "workflow_executions"
	RefetchConfigHealth       RefetchType = "config_health"
	RefetchPlanTasks          RefetchType = "plan_tasks"
)

// RefetchData is the JSON payload for refetch events.
type RefetchData struct {
	Type RefetchType `json:"type"`
}

// EmitUserRefetch emits a refetch signal via user_updates (global/user stream).
// Use this for worktree-scoped or project-scoped refetches.
func (r *Repo) EmitUserRefetch(ctx context.Context, userID string, refetchType RefetchType, opts RefetchOpts) error {
	data, err := json.Marshal(RefetchData{Type: refetchType})
	if err != nil {
		return fmt.Errorf("failed to marshal refetch data: %w", err)
	}

	entityType := EntityTypeSystem
	entityID := string(refetchType)

	if opts.WorktreeID != nil {
		entityType = EntityTypeWorktree
		entityID = *opts.WorktreeID
	} else if opts.ProjectID != nil {
		entityType = EntityTypeProject
		entityID = *opts.ProjectID
	}

	return r.CreateUserUpdate(ctx, &UserUpdate{
		UserID:     userID,
		ProjectID:  opts.ProjectID,
		WorktreeID: opts.WorktreeID,
		ChatID:     opts.ChatID,
		UpdateType: UserUpdateRefetch,
		EntityType: entityType,
		EntityID:   entityID,
		Data:       data,
	})
}

// EmitChatRefetch emits a refetch signal via chat_updates (per-chat stream).
// Use this for chat-scoped refetches like workflow_executions or plan_tasks.
func (r *Repo) EmitChatRefetch(ctx context.Context, chatID string, refetchType RefetchType) error {
	data, err := json.Marshal(RefetchData{Type: refetchType})
	if err != nil {
		return fmt.Errorf("failed to marshal refetch data: %w", err)
	}

	return r.CreateChatUpdate(ctx, chatID, UpdateTypeRefetch, "refetch-"+string(refetchType), string(data))
}

// RefetchOpts provides optional scoping for user-level refetch events.
type RefetchOpts struct {
	ProjectID  *string
	WorktreeID *string
	ChatID     *string
}
