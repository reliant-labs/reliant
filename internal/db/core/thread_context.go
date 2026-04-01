package core

import (
	"context"
	"time"
)

// Thread represents a conversation thread with optional fork relationships.
type Thread struct {
	ID                    string    `json:"id"`
	ConversationID        string    `json:"conversation_id"`
	ParentThreadID        *string   `json:"parent_thread_id,omitempty"`
	ForkAtOrdinal         *int64    `json:"fork_at_ordinal,omitempty"`
	ForkAtContextWindowID *string   `json:"fork_at_context_window_id,omitempty"`
	WorkflowID            *string   `json:"workflow_id,omitempty"`
	Title                 *string   `json:"title,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

// ContextWindow represents an atomic unit for LLM context.
type ContextWindow struct {
	ID                         string    `json:"id"`
	ThreadID                   string    `json:"thread_id"`
	Sequence                   int       `json:"sequence"`
	ParentContextWindowID      *string   `json:"parent_context_window_id,omitempty"`
	ForkAtOrdinal              *int64    `json:"fork_at_ordinal,omitempty"`
	CompactionSummaryMessageID *string   `json:"compaction_summary_message_id,omitempty"`
	CreatedAt                  time.Time `json:"created_at"`
}

// ThreadStore is the shared contract for thread persistence across drivers.
type ThreadStore interface {
	CreateThread(ctx context.Context, thread *Thread) (*Thread, error)
	GetThread(ctx context.Context, id string) (*Thread, error)
	GetThreadByWorkflow(ctx context.Context, workflowID string) (*Thread, error)
	GetRootThread(ctx context.Context, conversationID string) (*Thread, error)
	GetThreadWithParent(ctx context.Context, id string) (*Thread, *string, error)
	ListThreadsByConversation(ctx context.Context, conversationID string) ([]*Thread, error)
	ListChildThreads(ctx context.Context, parentThreadID string) ([]*Thread, error)
	UpdateThreadWorkflow(ctx context.Context, threadID, workflowID string) (*Thread, error)
	UpdateThreadForkPoint(ctx context.Context, threadID string, forkAtOrdinal *int64, forkAtContextWindowID *string) (*Thread, error)
	DeleteThread(ctx context.Context, id string) error
	DeleteThreadsByConversation(ctx context.Context, conversationID string) error
	CountThreadsInConversation(ctx context.Context, conversationID string) (int64, error)
}

// ContextWindowStore is the shared contract for context-window persistence across drivers.
type ContextWindowStore interface {
	CreateContextWindow(ctx context.Context, cw *ContextWindow) (*ContextWindow, error)
	GetContextWindow(ctx context.Context, id string) (*ContextWindow, error)
	GetLatestContextWindow(ctx context.Context, threadID string) (*ContextWindow, error)
	GetContextWindowBySequence(ctx context.Context, threadID string, sequence int) (*ContextWindow, error)
	GetContextWindowWithThread(ctx context.Context, id string) (*ContextWindow, string, *string, *int64, error)
	ListContextWindowsByThread(ctx context.Context, threadID string) ([]*ContextWindow, error)
	GetMaxSequenceForThread(ctx context.Context, threadID string) (int, error)
	SetCompactionSummaryMessage(ctx context.Context, cwID, messageID string) (*ContextWindow, error)
	DeleteContextWindow(ctx context.Context, id string) error
	DeleteContextWindowsByThread(ctx context.Context, threadID string) error
}
