package core

import (
	"context"
	"encoding/json"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ChatState represents the notification/lifecycle state of a chat.
type ChatState = reliantv1.ChatState

const (
	ChatStateUnspecified ChatState = reliantv1.ChatState_CHAT_STATE_UNSPECIFIED
	ChatStateIdle        ChatState = reliantv1.ChatState_CHAT_STATE_IDLE
	ChatStateArchived    ChatState = reliantv1.ChatState_CHAT_STATE_ARCHIVED
)

// WorkflowStatus represents the status of a workflow execution.
type WorkflowStatus = reliantv1.ChatWorkflowStatus

const (
	WorkflowStatusUnspecified WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_UNSPECIFIED
	WorkflowStatusPending     WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_PENDING
	WorkflowStatusRunning     WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_RUNNING
	WorkflowStatusCompleted   WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_COMPLETED
	WorkflowStatusFailed      WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_FAILED
	WorkflowStatusCancelled   WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_CANCELLED
	WorkflowStatusPaused      WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_PAUSED
	WorkflowStatusExpired     WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_EXPIRED
)

// WorkflowStatusIsLive reports whether a workflow run is still executing, or
// will execute again on its own. It is the complement of "has reached a state
// it will not leave", and it is what any caller asking "is there a next agent
// turn to deliver into" should consult.
//
// PENDING and PAUSED are deliberately live. PENDING is a chat whose run has
// not started yet — its first loop iteration is still ahead of it, so work
// queued now IS drained when it starts. PAUSED resumes and finishes. Treating
// either as dead would drop a message that would in fact have been delivered.
func WorkflowStatusIsLive(status WorkflowStatus) bool {
	switch status {
	case WorkflowStatusPending, WorkflowStatusRunning, WorkflowStatusPaused:
		return true
	default:
		return false
	}
}

// WorkflowStatusLabel renders a workflow status for display in user-facing
// messages, matching ThreadStatusLabel's role for threads.
func WorkflowStatusLabel(status WorkflowStatus) string {
	switch status {
	case WorkflowStatusPending:
		return "pending"
	case WorkflowStatusRunning:
		return "running"
	case WorkflowStatusCompleted:
		return "completed"
	case WorkflowStatusFailed:
		return "failed"
	case WorkflowStatusCancelled:
		return "cancelled"
	case WorkflowStatusPaused:
		return "paused"
	case WorkflowStatusExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// Chat represents a top-level conversation.
type Chat struct {
	ID                   string            `json:"id"`
	Title                string            `json:"title"`
	ProjectID            string            `json:"project_id"`
	WorktreeID           *string           `json:"worktree_id,omitempty"`
	ArchivedWorktreeName *string           `json:"archived_worktree_name,omitempty"`
	UserID               string            `json:"user_id"`
	WorkflowName         *string           `json:"workflow_name,omitempty"`
	State                ChatState         `json:"state"`
	WorkflowID           *string           `json:"workflow_id,omitempty"`
	RunID                *string           `json:"run_id,omitempty"`
	SelectedPresets      map[string]string `json:"selected_presets,omitempty"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	LastActive           time.Time         `json:"last_active"`
	LastMessageAt        *time.Time        `json:"last_message_at,omitempty"`
	Activity             *int              `json:"activity,omitempty"`
	Unread               bool              `json:"unread"`
	ActiveDaemonID       *string           `json:"active_daemon_id,omitempty"`
}

// MainThreadID returns the chat's root thread id, or "" if no root workflow
// has been created yet. The root workflow's thread id equals its workflow id.
func (c *Chat) MainThreadID() string {
	if c.WorkflowID != nil && *c.WorkflowID != "" {
		return *c.WorkflowID
	}
	return ""
}

// ArchivedChatInfo represents an archived chat with worktree information.
type ArchivedChatInfo struct {
	Chat
	WorktreeName      *string    `json:"worktree_name,omitempty"`
	WorktreeDeletedAt *time.Time `json:"worktree_deleted_at,omitempty"`
}

// ChatFilters contains options for filtering chats.
type ChatFilters struct {
	UserID          string
	ProjectID       *string
	State           *ChatState
	ExcludeArchived bool
	Limit           int
	Offset          int
}

// ChatSearchFilters contains options for searching chats.
type ChatSearchFilters struct {
	UserID      string
	ProjectID   string
	SearchQuery string
	State       *ChatState
	Limit       int
	Offset      int
}

// ChatUpdate represents an update in the chat_updates table.
type ChatUpdate struct {
	ID             string                   `json:"id"`
	ChatID         string                   `json:"chat_id"`
	SequenceNumber int64                    `json:"sequence_number"`
	UpdateType     reliantv1.ChatUpdateType `json:"update_type"`
	EntityID       string                   `json:"entity_id"`
	Data           json.RawMessage          `json:"data"`
	CreatedAt      time.Time                `json:"created_at"`
}

// ChatStore is the shared contract for chat persistence across drivers.
type ChatStore interface {
	CreateChat(ctx context.Context, chat *Chat) error
	GetChat(ctx context.Context, id string) (*Chat, error)
	GetChatWithUserCheck(ctx context.Context, id string, userID string) (*Chat, error)
	ListChats(ctx context.Context, filters ChatFilters) ([]*Chat, error)
	SearchChats(ctx context.Context, filters ChatSearchFilters) ([]*Chat, error)
	UpdateChat(ctx context.Context, chat *Chat) error
	DeleteChat(ctx context.Context, id string) error
	UpdateChatActiveDaemon(ctx context.Context, chatID string, daemonID *string) error
	ListArchivedChats(ctx context.Context, userID string) ([]*ArchivedChatInfo, error)
	CreateChatUpdate(ctx context.Context, update ChatUpdate) error
}
