package core

import (
	"context"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Message represents a conversation message.
// NOTE: Thread and ContextSequence have been removed - use ContextWindowID instead.
// To get thread/context_sequence, join with context_windows table.
type Message struct {
	ID              string
	ChatID          string
	Ordinal         int64
	ThreadID        string // Denormalized from context_window for efficiency
	ContextWindowID string // FK to context_windows table (required)
	Role            reliantv1.MessageRole
	DisplayStyle    *reliantv1.DisplayStyle // DisplayStyle proto enum value, or nil for default
	Model           *string
	Agent           *string
	TokenCount      *int     // Context size (tokens the LLM saw for this request)
	Cost            *float64 // Cost in USD
	WorkflowID      *string
	RunID           *string
	NodeID          *string // Workflow node/step identifier
	NodePath        *string // Full path to node in workflow hierarchy
	ActivityID      *string // Temporal activity ID for idempotency
	IsStreaming     bool    // Whether message is currently streaming

	CreatedAt time.Time
	UpdatedAt time.Time
}

// MessageContentBlock represents granular message content.
type MessageContentBlock struct {
	ID               string
	MessageID        string
	Position         int
	BlockType        reliantv1.ContentBlockType
	Content          *string
	ToolName         *string
	ToolInput        *string
	ToolCallID       *string
	ThoughtSignature *string // Gemini 3.x thought signature for maintaining reasoning context
	IsError          *bool
	Version          *int
	NodeID           string // Workflow node/step identifier
	NodePath         string // Full path to node in workflow hierarchy
	// Activity idempotency tracking (for Temporal activity retries)
	ActivityID    *string // Temporal activity ID that created this block
	WorkflowRunID *string // Temporal workflow run ID
	AttemptNumber int     // Activity attempt number (starts at 1, increments on retry)
	IsComplete    bool    // Whether streaming is complete for this block
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// MessageListOptions contains options for filtering messages.
// NOTE: Thread is the thread ID (from threads table). ContextSequence is the
// context_windows.sequence value. These are used to filter via context_windows join.
type MessageListOptions struct {
	Thread          *string // Thread ID to filter by (via context_windows)
	ContextSequence *int    // Context window sequence to filter by
	ContextWindowID *string // Direct context window ID filter (takes precedence)
	Limit           int
	Offset          int
}

// MessageStore is the shared contract for message persistence across drivers.
type MessageStore interface {
	CreateMessage(ctx context.Context, msg *Message) error
	CreateMessageIfNotExists(ctx context.Context, msg *Message) error
	GetMessage(ctx context.Context, id string) (*Message, error)
	GetNextOrdinal(ctx context.Context, threadID string) (int64, error)
	ListMessages(ctx context.Context, chatID string, opts MessageListOptions, listContextWindowIDsByThread func(context.Context, string) ([]string, error)) ([]*Message, error)
	GetLatestMessageInThread(ctx context.Context, threadID string) (*Message, error)
	GetLatestContextSequenceByThread(ctx context.Context, threadID string) (int64, error)
	GetLatestMessageWithTokensInThread(ctx context.Context, threadID string, contextSequence int) (*Message, error)
	CountMessagesInThread(ctx context.Context, threadID string) (int, error)
	CreateContentBlock(ctx context.Context, block *MessageContentBlock) error
	CreateContentBlockIfNotExists(ctx context.Context, block *MessageContentBlock) error
	GetContentBlock(ctx context.Context, id string) (*MessageContentBlock, error)
	ListContentBlocks(ctx context.Context, messageID string) ([]*MessageContentBlock, error)
	ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*MessageContentBlock, error)
	UpdateContentBlock(ctx context.Context, block *MessageContentBlock) error
	AppendToContentBlock(ctx context.Context, blockID string, delta string) error
	UpdateMessage(ctx context.Context, msg *Message) error
}
