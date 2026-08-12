package core

import (
	"context"
	"time"
)

// ThreadOrigin describes how a thread came to exist. It answers a different
// question than Workflow.SpawnedByNodeID, which records WHICH graph node
// produced a workflow — provenance, not kind. Conflating the two is what
// previously forced readers to string-match a node-ID field against the
// sentinel "spawn_tool" to recognize a spawn.
//
// Origin replaced an earlier threads.kind column (ROOT/BRANCH/SUBAGENT). Kind
// answered "where does this sit in the lineage" and could not tell a spawn
// from a graph-node child; origin answers "what made this" and can. The one
// distinction kind drew that origin does not is cross-chat branch vs
// same-chat fork — that is recoverable from stored, checkable facts
// (origin == fork AND parent.chat_id != chat_id), which is what ListBranches
// now compares.
type ThreadOrigin = string

const (
	// ThreadOriginMain is the chat's root thread. It has no parent.
	ThreadOriginMain ThreadOrigin = "main"
	// ThreadOriginSpawn is a thread created by the spawn tool.
	ThreadOriginSpawn ThreadOrigin = "spawn"
	// ThreadOriginFork is a thread forked from a parent thread. A fork whose
	// parent lives in a different chat is what the UI calls a branch.
	ThreadOriginFork ThreadOrigin = "fork"
	// ThreadOriginNode is a thread created by a workflow graph node; the node
	// is named by Thread.OriginNodeID (which may be nil for threads migrated
	// from before origins were recorded).
	ThreadOriginNode ThreadOrigin = "node"
)

// Thread lifecycle statuses. These mirror CHAT_WORKFLOW_STATUS so a thread's
// state is directly comparable to its workflow's.
const (
	ThreadStatusRunning   int32 = 2
	ThreadStatusCompleted int32 = 3
	ThreadStatusFailed    int32 = 4
	ThreadStatusCancelled int32 = 5
	ThreadStatusExpired   int32 = 7
)

// ThreadStatusIsTerminal reports whether a thread's loop has exited and
// there is nothing left to deliver a message into. Shared by every path that
// queues into a thread's mailbox (spawn_send, the human-facing
// SendAgentMessage RPC) so "has this agent finished" is answered the same
// way everywhere.
func ThreadStatusIsTerminal(status int32) bool {
	switch status {
	case ThreadStatusCompleted, ThreadStatusFailed, ThreadStatusCancelled, ThreadStatusExpired:
		return true
	default:
		return false
	}
}

// ThreadStatusLabel renders a thread status for display in error messages.
func ThreadStatusLabel(status int32) string {
	switch status {
	case ThreadStatusRunning:
		return "running"
	case ThreadStatusCompleted:
		return "completed"
	case ThreadStatusFailed:
		return "failed"
	case ThreadStatusCancelled:
		return "cancelled"
	case ThreadStatusExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// Thread represents a conversation thread with optional fork relationships.
type Thread struct {
	ID             string  `json:"id"`
	ChatID         string  `json:"chat_id"`
	ParentThreadID *string `json:"parent_thread_id,omitempty"`
	// ForkAtMessageID is the last message of the parent thread this thread
	// inherited: everything up to and including it is visible here,
	// everything after it is not. Nil on a thread that is not a fork.
	// It replaced a (fork_at_ordinal, fork_at_context_window_id) pair --
	// the message already knows its own context window, and a foreign key
	// is checkable where an offset was not. See
	// 20260803010000_fork_points_reference_messages.sql.
	ForkAtMessageID *string   `json:"fork_at_message_id,omitempty"`
	WorkflowID      *string   `json:"workflow_id,omitempty"`
	Title           *string   `json:"title,omitempty"`
	CreatedAt       time.Time `json:"created_at"`

	// Origin is how this thread was created; see ThreadOrigin.
	Origin ThreadOrigin `json:"origin"`
	// OriginNodeID names the graph node that created the thread. Set only for
	// ThreadOriginNode, where the node ID is genuine provenance.
	OriginNodeID *string `json:"origin_node_id,omitempty"`

	// Status is the thread's lifecycle state, owned by the thread itself
	// rather than by a synthetic "thread:<node>" workflow record.
	Status      int32      `json:"status"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// ContextWindow represents an atomic unit for LLM context.
type ContextWindow struct {
	ID                    string  `json:"id"`
	ThreadID              string  `json:"thread_id"`
	Sequence              int     `json:"sequence"`
	ParentContextWindowID *string `json:"parent_context_window_id,omitempty"`
	// ForkAtMessageID bounds what this window inherits from its parent
	// window: the parent's messages up to and including this one. Nil means
	// inherit the parent unfiltered (a compaction link, not a branch).
	ForkAtMessageID            *string   `json:"fork_at_message_id,omitempty"`
	CompactionSummaryMessageID *string   `json:"compaction_summary_message_id,omitempty"`
	CreatedAt                  time.Time `json:"created_at"`
}

// ThreadStore is the shared contract for thread persistence across drivers.
type ThreadStore interface {
	CreateThread(ctx context.Context, thread *Thread) (*Thread, error)
	GetThread(ctx context.Context, id string) (*Thread, error)
	GetThreadByWorkflow(ctx context.Context, workflowID string) (*Thread, error)
	GetRootThread(ctx context.Context, chatID string) (*Thread, error)
	GetThreadWithParent(ctx context.Context, id string) (*Thread, *string, error)
	ListThreadsByConversation(ctx context.Context, chatID string) ([]*Thread, error)
	ListChildThreads(ctx context.Context, parentThreadID string) ([]*Thread, error)
	UpdateThreadWorkflow(ctx context.Context, threadID, workflowID string) (*Thread, error)
	UpdateThreadForkPoint(ctx context.Context, threadID string, forkAtMessageID *string) (*Thread, error)
	UpdateThreadStatus(ctx context.Context, threadID string, status int32, completedAt *time.Time) (*Thread, error)
	ListThreadsByOrigin(ctx context.Context, chatID string, origin ThreadOrigin) ([]*Thread, error)
	DeleteThread(ctx context.Context, id string) error
	DeleteThreadsByConversation(ctx context.Context, chatID string) error
	CountThreadsInConversation(ctx context.Context, chatID string) (int64, error)
}

// ContextWindowStore is the shared contract for context-window persistence across drivers.
type ContextWindowStore interface {
	CreateContextWindow(ctx context.Context, cw *ContextWindow) (*ContextWindow, error)
	GetContextWindow(ctx context.Context, id string) (*ContextWindow, error)
	GetLatestContextWindow(ctx context.Context, threadID string) (*ContextWindow, error)
	GetContextWindowBySequence(ctx context.Context, threadID string, sequence int) (*ContextWindow, error)
	GetContextWindowWithThread(ctx context.Context, id string) (*ContextWindow, string, *string, *string, error)
	ListContextWindowsByThread(ctx context.Context, threadID string) ([]*ContextWindow, error)
	GetMaxSequenceForThread(ctx context.Context, threadID string) (int, error)
	SetCompactionSummaryMessage(ctx context.Context, cwID, messageID string) (*ContextWindow, error)
	DeleteContextWindow(ctx context.Context, id string) error
	DeleteContextWindowsByThread(ctx context.Context, threadID string) error
}
