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

// WorkflowState is where a run IS; WorkflowStopReason is why a stopped run
// stopped. They are always read together — see WorkflowStatus below, which is
// the pair, and the Live/Resumable predicates, which are what callers should
// actually consult instead of comparing these directly.
type (
	WorkflowState      = reliantv1.WorkflowState
	WorkflowStopReason = reliantv1.WorkflowStopReason
)

const (
	WorkflowStateUnspecified WorkflowState = reliantv1.WorkflowState_WORKFLOW_STATE_UNSPECIFIED
	WorkflowStatePending     WorkflowState = reliantv1.WorkflowState_WORKFLOW_STATE_PENDING
	WorkflowStateActive      WorkflowState = reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE
	WorkflowStateStopped     WorkflowState = reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED

	StopReasonUnspecified WorkflowStopReason = reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_UNSPECIFIED
	StopReasonCompleted   WorkflowStopReason = reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED
	StopReasonFailed      WorkflowStopReason = reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_FAILED
	StopReasonPaused      WorkflowStopReason = reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_PAUSED
	StopReasonCancelled   WorkflowStopReason = reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_CANCELLED
)

// WorkflowStatus is a run's lifecycle: the state, plus the reason it stopped.
// The two travel together because neither answers a useful question alone —
// STOPPED without a reason cannot distinguish a finished run from a parked
// one, and a reason without a state is meaningless.
//
// Prefer the predicates below to comparing these fields. Most callers do not
// actually care which of five ways a run stopped; they care whether more work
// is coming (Live) or whether there is a position to continue from
// (Resumable). Those two questions are what the old eight-value enum was being
// asked, one ad-hoc comparison at a time.
type WorkflowStatus struct {
	State      WorkflowState      `json:"state"`
	StopReason WorkflowStopReason `json:"stop_reason"`
}

// Constructors for the states a run can actually be in. Using these rather
// than struct literals keeps the invariant "a reason accompanies STOPPED and
// only STOPPED" in one place.
func Pending() WorkflowStatus {
	return WorkflowStatus{State: WorkflowStatePending}
}

func Active() WorkflowStatus {
	return WorkflowStatus{State: WorkflowStateActive}
}

func Stopped(reason WorkflowStopReason) WorkflowStatus {
	return WorkflowStatus{State: WorkflowStateStopped, StopReason: reason}
}

// Named stopped statuses, for the five terminal writes the system performs.
func Completed() WorkflowStatus { return Stopped(StopReasonCompleted) }
func Failed() WorkflowStatus    { return Stopped(StopReasonFailed) }
func Paused() WorkflowStatus    { return Stopped(StopReasonPaused) }
func Cancelled() WorkflowStatus { return Stopped(StopReasonCancelled) }

// IsStopped reports whether the run is not executing, whatever the reason.
func (s WorkflowStatus) IsStopped() bool { return s.State == WorkflowStateStopped }

// Live reports whether a run is still executing, or will execute again on its
// own. It is what any caller asking "is there a next agent turn to deliver
// into" should consult.
//
// PENDING and PAUSED are deliberately live. PENDING is a chat whose run has
// not started yet — its first loop iteration is still ahead of it, so work
// queued now IS drained when it starts. PAUSED resumes and finishes. Treating
// either as dead would drop a message that would in fact have been delivered.
func (s WorkflowStatus) Live() bool {
	switch s.State {
	case WorkflowStatePending, WorkflowStateActive:
		return true
	case WorkflowStateStopped:
		return s.StopReason == StopReasonPaused
	default:
		return false
	}
}

// Executable reports whether work may run for this status RIGHT NOW.
//
// This is deliberately NOT Live(), and the difference is the whole point.
// Live() answers "will this run produce another turn" — a queueing question,
// for which PAUSED is correctly alive, because a paused run resumes and drains
// what was queued. Executable answers "should an activity execute this
// instant", and a paused run must answer no: the entire purpose of pausing is
// that work stops.
//
// Conflating the two is not hypothetical. A paused run kept issuing LLM calls
// because the only predicate available treated PAUSED as alive: on chat
// 128cf4f5 a retry-exhaustion self-pause resumed at 17:41:51, re-ran the same
// failing step, and failed identically at 17:42:08, with the workflow row at
// STOPPED/PAUSED throughout. Every one of those turns was work issued by a run
// that was not running.
//
// PENDING is executable: its run has not started, and starting is exactly what
// its first activity does. Only STOPPED — for any reason — is not.
func (s WorkflowStatus) Executable() bool {
	return s.State != WorkflowStateStopped
}

// Resumable reports whether a stopped run may have a position to continue
// from, so the next message continues it rather than starting a new run.
//
// A COMPLETED run has no position left — it reached a terminal node, so there
// is nowhere to resume TO, and starting fresh is correct rather than a
// fallback. A CANCELLED run had its position deliberately dropped by the hard
// stop; resuming it would defeat the point of the verb. Everything else that
// stopped did so mid-run and kept its checkpoint.
func (s WorkflowStatus) Resumable() bool {
	if s.State != WorkflowStateStopped {
		return false
	}
	switch s.StopReason {
	case StopReasonCompleted, StopReasonCancelled:
		return false
	default:
		return true
	}
}

// Label renders a status for display in user-facing messages, matching
// ThreadStatusLabel's role for threads.
func (s WorkflowStatus) Label() string {
	switch s.State {
	case WorkflowStatePending:
		return "pending"
	case WorkflowStateActive:
		return "running"
	case WorkflowStateStopped:
		switch s.StopReason {
		case StopReasonCompleted:
			return "completed"
		case StopReasonFailed:
			return "failed"
		case StopReasonPaused:
			return "paused"
		case StopReasonCancelled:
			return "cancelled"
		default:
			return "stopped"
		}
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
