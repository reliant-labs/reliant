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
)

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
