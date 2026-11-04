package core

import (
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
	// ProviderAccountID is the provider account that served an assistant
	// message; nil before accounts were recorded and for other roles.
	ProviderAccountID *string
	// SenderThreadID and SenderKind attribute a message another agent wrote
	// (a spawn_send, say) to its sender. Both nil means the human, or the
	// model's own output. SenderThreadID is not a foreign key: a message
	// outlives its sender's thread.
	SenderThreadID *string
	SenderKind     *reliantv1.AgentMessageKind
	// SenderSubject is the one-line subject the sending agent gave the
	// message (spawn_send's `subject`); nil when it gave none.
	SenderSubject *string

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
	// Phase is the OpenAI Responses `phase` of a TEXT block from an assistant
	// message — "commentary" or "final_answer". Nil on every other provider
	// and on blocks saved before it was captured. Carried so the next turn can
	// resend it (responseswire.AssistantHistoryItem).
	Phase    *string
	IsError  *bool
	Version  *int
	NodeID   string // Workflow node/step identifier
	NodePath string // Full path to node in workflow hierarchy
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
