package core

import (
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

// ThreadStatus is a thread's lifecycle state (threads.status). The values
// mirror the retired CHAT_WORKFLOW_STATUS numbering so a thread's state reads
// the same as the workflow statuses it was split from.
type ThreadStatus int32

// Thread lifecycle statuses.
const (
	ThreadStatusRunning   ThreadStatus = 2
	ThreadStatusCompleted ThreadStatus = 3
	ThreadStatusFailed    ThreadStatus = 4
	ThreadStatusCancelled ThreadStatus = 5
	// Value 7 (expired) is retired: nothing ever wrote it. Temporal's
	// TIMED_OUT is deliberately recorded as a failure.
)

// ThreadStatusForStopReason maps a workflow's reason for stopping onto the
// thread status its owned threads should take. Threads keep their own flat
// status column — they have no PENDING/ACTIVE distinction to make — so this is
// the one place the two vocabularies meet.
//
// A run that stopped only because it is PAUSED has not ended, and its threads
// stay running: closing them would make a resumable run's threads read as
// finished, which is what ThreadStatusIsTerminal's callers use to refuse
// message delivery.
func ThreadStatusForStopReason(reason WorkflowStopReason) (status ThreadStatus, terminal bool) {
	switch reason {
	case StopReasonCompleted:
		return ThreadStatusCompleted, true
	case StopReasonFailed:
		return ThreadStatusFailed, true
	case StopReasonCancelled:
		return ThreadStatusCancelled, true
	default:
		return ThreadStatusRunning, false
	}
}

// ThreadStatusIsTerminal reports whether a thread's loop has exited and
// there is nothing left to deliver a message into. Shared by every path that
// queues into a thread's mailbox (spawn_send, the human-facing
// SendAgentMessage RPC) so "has this agent finished" is answered the same
// way everywhere.
func ThreadStatusIsTerminal(status ThreadStatus) bool {
	switch status {
	case ThreadStatusCompleted, ThreadStatusFailed, ThreadStatusCancelled:
		return true
	default:
		return false
	}
}

// ThreadStatusLabel renders a thread status for display in error messages.
func ThreadStatusLabel(status ThreadStatus) string {
	switch status {
	case ThreadStatusRunning:
		return "running"
	case ThreadStatusCompleted:
		return "completed"
	case ThreadStatusFailed:
		return "failed"
	case ThreadStatusCancelled:
		return "cancelled"
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
	Status      ThreadStatus `json:"status"`
	CompletedAt *time.Time   `json:"completed_at,omitempty"`

	// WorktreeID is the workspace this thread's tools run in when it is not
	// the chat's: set on a sub-agent spawned with a `worktree`. Nil means the
	// chat's own worktree (Chat.WorktreeID).
	WorktreeID *string `json:"worktree_id,omitempty"`
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

// ReapedThread is one thread ReapOrphanedThreads closed: the thread, its chat,
// the workflow whose terminal status it was missing, and the status it took.
type ReapedThread struct {
	ThreadID   string
	ChatID     string
	WorkflowID string
	Status     ThreadStatus
}
