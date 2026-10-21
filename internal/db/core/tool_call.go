package core

import (
	"errors"
	"time"
)

// ToolCallStatus is the durable lifecycle state of a tool call.
//
// These values are the wire contract for the tool_calls.status column and are
// mirrored by a proto enum of the same shape. Values are fixed: changing one
// silently reinterprets every row already in the database.
type ToolCallStatus int32

const (
	// ToolCallStatusUnspecified is the zero value; a persisted row should
	// never carry it.
	ToolCallStatusUnspecified ToolCallStatus = 0
	// ToolCallStatusPending means the call has been requested but execution
	// has not begun.
	ToolCallStatusPending ToolCallStatus = 1
	// ToolCallStatusExecuting means the tool is currently running.
	ToolCallStatusExecuting ToolCallStatus = 2
	// ToolCallStatusCompleted means the tool finished and produced a result.
	// A row in this state must have CompletedAt set (enforced by a CHECK
	// constraint).
	ToolCallStatusCompleted ToolCallStatus = 3
	// ToolCallStatusFailed means the tool ran and returned an error result.
	ToolCallStatusFailed ToolCallStatus = 4
	// ToolCallStatusCancelled means the call will never produce a result --
	// the workflow was cancelled, or the call is a historical one that
	// predates durable status and has no result block.
	ToolCallStatusCancelled ToolCallStatus = 5
	// ToolCallStatusBackgrounded means execution was handed to a background
	// process; BackgroundProcessID identifies it.
	ToolCallStatusBackgrounded ToolCallStatus = 6
)

// IsTerminal reports whether the status is one a call will not move out of on
// its own. Backgrounded is deliberately NOT terminal: the process is still
// running and will report a real outcome later.
//
// Callers use this to decide whether a status write must survive cancellation
// of the request context — a terminal status is precisely the one being written
// when a workflow is being torn down, so losing it strands the row (and the UI)
// at "executing" permanently.
func (s ToolCallStatus) IsTerminal() bool {
	switch s {
	case ToolCallStatusCompleted, ToolCallStatusFailed, ToolCallStatusCancelled:
		return true
	default:
		return false
	}
}

// ToolCall is a durable record of a tool invocation requested by the LLM.
//
// Before this existed, tool status lived only in transient chat_updates
// events, so a reader after a reload had to infer whether a call had finished
// from whether its workflow was still running -- which is wrong for any
// workflow that paused mid-call. The row makes the status a fact.
type ToolCall struct {
	// ID is the id the LLM assigned to the call (e.g. "toolu_01..."), not a
	// generated surrogate: it is the only identifier the provider echoes back
	// on the matching tool_result, so it is what the result must join on.
	ID     string
	ChatID string
	// ThreadID and MessageID are optional because a call can be recorded
	// before the assistant message that carries it is finalized.
	ThreadID  *string
	MessageID *string
	ToolName  string
	// Input is the tool's JSON arguments. Nil while streaming, before the
	// arguments have finished arriving.
	Input        []byte
	Status       ToolCallStatus
	ErrorMessage *string
	// ChildWorkflowID is set for calls that spawn a sub-workflow.
	ChildWorkflowID *string
	// BackgroundProcessID is set for calls moved to background execution.
	BackgroundProcessID *string
	// DaemonID is the daemon running a backgrounded call's process: the only
	// party that can say when that process ends. Stamped by the daemon gateway
	// from the connection that answered, so it names the machine that actually
	// ran the tool rather than the user's default one.
	DaemonID    *string
	RequestedAt time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ToolCallResult is the result the tool produced, keyed by the call it
// answers.
//
// ToolCallID is the primary key rather than a surrogate id, and it is a
// foreign key into tool_calls. That is deliberate and load-bearing: the
// product cannot send the LLM an assistant message whose tool_use blocks lack
// matching tool_result blocks without deadlocking the provider, and this pair
// of constraints makes a duplicate result and an orphaned result both
// unrepresentable rather than something repair code has to detect later.
type ToolCallResult struct {
	ToolCallID string
	// MessageID is the tool-role message carrying the result; optional
	// because the result can be recorded before that message exists.
	MessageID *string
	Content   string
	IsError   bool
	// GrantedTools are the tools this result granted the call's thread — a
	// load_tool result's loaded names, empty for every other result. Written
	// with Content, so it always describes the result the model reads.
	GrantedTools []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ErrToolCallIDInAnotherChat is returned by a tool call or result write whose
// id is held by a call recorded for a different chat.
//
// The id is the model provider's, not ours, and nothing makes a provider pick
// ids that are unique across conversations: a local OpenAI-compatible server
// can answer `call_0` in every chat it serves. The row belongs to the chat that
// recorded it first, and a second chat's write is refused rather than merged
// into it — merging is what used to carry one chat's input, status and output
// into another chat's transcript. The refused chat's call still runs and its
// result still reaches its own conversation (that lives in its messages); it
// only goes without the durable status row.
var ErrToolCallIDInAnotherChat = errors.New("tool call id is already used by a call in another chat")

// StrandedBackgroundSpawn is one backgrounded spawn call whose child
// workflow reached a terminal status while the call itself is still marked
// backgrounded.
type StrandedBackgroundSpawn struct {
	ToolCallID string
	ChatID     string
	// ParentThreadID is the thread that issued the spawn -- the mailbox
	// recipient for the completion report. Nilable because tool_calls.thread_id
	// is nilable (a call can be recorded before its message is finalized),
	// though a spawn old enough to be stranded will have it set in practice.
	ParentThreadID *string
	ChildThreadID  string
	WorkflowStatus WorkflowStatus
	// HasReport is true when the parent's mailbox already holds a terminal
	// report for this call. The report then must not be written again, but
	// the call still has to be closed: the two are written by different code,
	// so a delivered report never implied a closed row.
	HasReport bool
}

// BackgroundedProcessCall is one backgrounded tool call whose outcome lives
// in a daemon's process table, and the facts needed to ask that daemon about
// it.
type BackgroundedProcessCall struct {
	ToolCallID string
	ChatID     string
	ToolName   string
	// UserID owns the chat, and therefore the daemon the process runs on.
	UserID string
	// BackgroundProcessID and DaemonID say which process on which daemon.
	// Either may be nil on a row written before it was recorded.
	BackgroundProcessID *string
	DaemonID            *string
	RequestedAt         time.Time
}
