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
	ID     string
	ChatID string
	// Ordinal is the per-thread insertion order. Still written on every
	// create (see GetNextOrdinalByThread), but no longer used by the read
	// path — see Seq. Kept for one more phase as revert insurance; do not
	// drop the column or the writer.
	Ordinal int64
	// Seq is the chat-global total order (dense, per chat_id), populated
	// alongside Ordinal by 20260802000000_add_message_seq.sql. This is the
	// canonical ordering for all message reads.
	Seq             int64
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
	// GetNextSeq returns the next seq for a message being written to threadID in
	// chatID. threadID is required because a branched chat displays inherited
	// history it does not own; the allocator has to clear that history's seqs or
	// the branch restarts at 0 and new messages sort beneath everything shown
	// above them. See 20260802000000_add_message_seq.sql.
	GetNextSeq(ctx context.Context, chatID, threadID string) (int64, error)
	ListMessages(ctx context.Context, chatID string, opts MessageListOptions, listContextWindowIDsByThread func(context.Context, string) ([]string, error)) ([]*Message, error)
	// ListRecentMessages returns the most recent `limit` messages for a chat in
	// ascending order, bounding the read in SQL rather than fetching the whole
	// history and slicing in memory the way ListMessages does.
	ListRecentMessages(ctx context.Context, chatID string, limit int) ([]*Message, error)
	// ListRecentChatWindow returns the newest `limit` messages on mainThreadID
	// plus every sibling-thread message inside that seq range, ascending. This
	// is the correct bound for the initial snapshot: spawn threads out-write and
	// out-live the main thread, so a chat-wide newest-N can be entirely spawn
	// messages, which render inside their tool call rather than the transcript.
	ListRecentChatWindow(ctx context.Context, chatID, mainThreadID string, limit int) ([]*Message, error)
	// CountMessagesInChat is the true total, so a bounded read can still report
	// an honest count.
	CountMessagesInChat(ctx context.Context, chatID string) (int, error)
	GetLatestMessageInThread(ctx context.Context, threadID string) (*Message, error)
	GetLatestContextSequenceByThread(ctx context.Context, threadID string) (int64, error)
	GetLatestMessageWithTokensInThread(ctx context.Context, threadID string, contextSequence int) (*Message, error)
	CountMessagesInThread(ctx context.Context, threadID string) (int, error)
	// CountMessagesByContextWindow is the row count of a single context
	// window, for CW-chain-aware totals that don't require fetching rows.
	CountMessagesByContextWindow(ctx context.Context, contextWindowID string) (int, error)
	// CountMessagesByContextWindowUpToSeq counts messages in a context window
	// with seq <= maxSeq -- the count-only mirror of the fork filter in
	// resolveMessagesFromCW (a forked child inherits its direct parent CW's
	// messages only up to the fork point).
	CountMessagesByContextWindowUpToSeq(ctx context.Context, contextWindowID string, maxSeq int64) (int, error)
	// ListRecentMessagesInContextWindowBeforeSeq returns the newest `limit`
	// messages in a single context window strictly before beforeSeq (0 means
	// unbounded), ascending. Bounds the read in SQL for the common (unforked)
	// case instead of fetching the whole context window.
	ListRecentMessagesInContextWindowBeforeSeq(ctx context.Context, contextWindowID string, beforeSeq int64, limit int) ([]*Message, error)
	// HasMessagesBeforeInContextWindow reports whether any message in this
	// context window precedes beforeSeq, for computing hasMore without
	// fetching rows.
	HasMessagesBeforeInContextWindow(ctx context.Context, contextWindowID string, beforeSeq int64) (bool, error)
	// ListMessagesInContextWindowRange returns messages in a single context
	// window with seq >= fromSeq, ascending. toSeq is an exclusive upper
	// bound, or nil for unbounded above.
	ListMessagesInContextWindowRange(ctx context.Context, contextWindowID string, fromSeq int64, toSeq *int64) ([]*Message, error)
	CreateContentBlock(ctx context.Context, block *MessageContentBlock) error
	CreateContentBlockIfNotExists(ctx context.Context, block *MessageContentBlock) error
	GetContentBlock(ctx context.Context, id string) (*MessageContentBlock, error)
	ListContentBlocks(ctx context.Context, messageID string) ([]*MessageContentBlock, error)
	ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*MessageContentBlock, error)
	UpdateContentBlock(ctx context.Context, block *MessageContentBlock) error
	AppendToContentBlock(ctx context.Context, blockID string, delta string) error
	UpdateMessage(ctx context.Context, msg *Message) error
}
