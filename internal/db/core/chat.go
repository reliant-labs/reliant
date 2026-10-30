package core

import (
	"encoding/json"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ErrChatNotFound is returned by chat reads when no row matches. Anything else
// a chat read returns is a store failure, which callers must not mistake for
// absence: "not found" is final, a database error is worth retrying.
//
// It is a svcerr NotFound, so a handler's svcerr.Wrap answers CodeNotFound
// "chat not found" — and errors.Is(err, ErrChatNotFound) still holds.
var ErrChatNotFound = svcerr.NotFound("chat")

// ChatState represents the notification/lifecycle state of a chat.
type ChatState = reliantv1.ChatState

const (
	ChatStateUnspecified ChatState = reliantv1.ChatState_CHAT_STATE_UNSPECIFIED
	ChatStateIdle        ChatState = reliantv1.ChatState_CHAT_STATE_IDLE
	ChatStateArchived    ChatState = reliantv1.ChatState_CHAT_STATE_ARCHIVED
)

// ChatOrigin is who started a chat (chats.origin). It is the chat list's
// policy key: a USER chat lists in the sidebar, any other origin is a run that
// lists only once adopted (chats_with_activity.list_in_sidebar).
type ChatOrigin = reliantv1.ChatOrigin

const (
	ChatOriginUnspecified ChatOrigin = reliantv1.ChatOrigin_CHAT_ORIGIN_UNSPECIFIED
	ChatOriginUser        ChatOrigin = reliantv1.ChatOrigin_CHAT_ORIGIN_USER
	ChatOriginAutomation  ChatOrigin = reliantv1.ChatOrigin_CHAT_ORIGIN_AUTOMATION
	ChatOriginAgent       ChatOrigin = reliantv1.ChatOrigin_CHAT_ORIGIN_AGENT
	ChatOriginManualRun   ChatOrigin = reliantv1.ChatOrigin_CHAT_ORIGIN_MANUAL_RUN
	ChatOriginBuilderTest ChatOrigin = reliantv1.ChatOrigin_CHAT_ORIGIN_BUILDER_TEST
	ChatOriginCLI         ChatOrigin = reliantv1.ChatOrigin_CHAT_ORIGIN_CLI
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
	// NoMachine records that this chat's runs have no machine by design: set
	// at launch, never inferred from a daemon being absent (an absent pinned
	// daemon is asleep and gets woken). Every run in the chat is offered only
	// tools that run without the user's machine. See research/DAEMONLESS_RUNS.md.
	NoMachine bool `json:"no_machine,omitempty"`
	// AdoptedAt is when the user adopted this run into their chats; nil if not.
	AdoptedAt *time.Time `json:"adopted_at,omitempty"`
	// ListInSidebar is the view's sidebar policy for this chat: Origin is USER,
	// or the user adopted the run.
	ListInSidebar bool `json:"list_in_sidebar"`
	// Origin is who started the chat, written once at launch (ChatOriginOf the
	// launch event's kind; USER for a branch). Unspecified on insert stores
	// USER, the column's default.
	Origin ChatOrigin `json:"origin,omitempty"`

	// UsageLimitResumesAt is when the chat's root run retries, while it is parked
	// until a provider's usage limit resets (activity 7); nil otherwise.
	UsageLimitResumesAt *time.Time `json:"usage_limit_resumes_at,omitempty"`
	// UsageLimitProvider names who imposed the limit; empty when unknown.
	UsageLimitProvider string `json:"usage_limit_provider,omitempty"`
	// UsageLimitProbing is true when the provider named no reset, so
	// UsageLimitResumesAt is only the next probe.
	UsageLimitProbing bool `json:"usage_limit_probing,omitempty"`

	// WorkingThreads is how many of the chat's threads are doing work right
	// now: a live run not blocked on the user and not only waiting on its own
	// sub-agents. Orthogonal to Activity — an AWAITING_INPUT chat with
	// WorkingThreads > 0 needs the user and is still running.
	WorkingThreads int `json:"working_threads,omitempty"`

	// RootStatus is the lifecycle of the chat's ROOT workflow — the row whose
	// id is WorkflowID. It travels with every chat read because callers that
	// ask "is this chat paused" or "has this chat started yet" are asking
	// about that one run, and looking it up separately made the question an
	// N+1 that most call sites simply skipped: Chat.workflow_state on the wire
	// was never populated at all, so the web's paused detection was
	// permanently false.
	//
	// A chat with no root workflow row (a branch that has not started) leaves
	// this at the zero WorkflowStatus, i.e. WORKFLOW_STATE_UNSPECIFIED. That
	// is distinct from PENDING, which is a root row that exists and has not
	// begun.
	RootStatus WorkflowStatus `json:"root_status"`

	// LaunchKind is the kind of the chat's launch event ("chat.start",
	// "schedule"); empty for a chat that predates trigger events. TriggerID is
	// the stored trigger that fired it, nil for ad hoc kinds.
	LaunchKind string  `json:"launch_kind,omitempty"`
	TriggerID  *string `json:"trigger_id,omitempty"`
}

// MainThreadID returns the chat's root thread id, or "" if no root workflow
// has been created yet. The root workflow's thread id equals its workflow id.
func (c *Chat) MainThreadID() string {
	if c.WorkflowID != nil && *c.WorkflowID != "" {
		return *c.WorkflowID
	}
	return ""
}

// ChatRow is a chat's own row, the chats table alone: who it belongs to, its
// lifecycle, root workflow, workspace and machine flags. It is what an
// ownership check reads. Chat is read from the chats_with_activity view, whose
// activity, last-message, root-workflow-state, working-thread, usage-limit and
// launch columns are subqueries over the chat's messages, workflows, threads
// and trigger events — a cost every RPC that only authorizes paid; such a
// caller reads this instead, and one that needs a derived field reads Chat.
type ChatRow struct {
	ID             string
	Title          string
	ProjectID      string
	UserID         string
	State          ChatState
	WorkflowID     *string
	WorkflowName   *string
	RunID          *string
	WorktreeID     *string
	ActiveDaemonID *string
	Unread         bool
	NoMachine      bool
	AdoptedAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastActive     time.Time
}

// MainThreadID is Chat.MainThreadID for the row's root workflow.
func (c *ChatRow) MainThreadID() string {
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
	// SidebarOnly keeps only chats the sidebar lists: the view's
	// list_in_sidebar column is the single definition of that policy. The
	// ListChats rpc always sets it; internal sweeps (a worktree's chats) do
	// not, because they act on runs too.
	SidebarOnly bool
	Limit       int
	Offset      int
}

// ChatSearchFilters contains options for searching chats.
type ChatSearchFilters struct {
	UserID string
	// ProjectID narrows the search to one project; empty searches all of the
	// user's projects.
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

// QueuedForMachineChat is a chat holding a message queued for its machine: its
// last run ended because the machine never came up, and the message it was
// started for is still owed a reply (chats.queued_for_machine_at).
type QueuedForMachineChat struct {
	ChatID string
	UserID string
	// ActiveDaemonID is the machine the chat is pinned to; nil means the
	// user's default machine.
	ActiveDaemonID *string
	// Machine is the machine the run that queued the message was waiting
	// for (chats.queued_for_machine_selector). It wins over ActiveDaemonID:
	// a workflow-level daemon selector targets a machine the chat's pin does
	// not name. Nil when the run named none.
	Machine  *QueuedMachine
	QueuedAt time.Time
}

// QueuedMachine is the machine a queued message waits for, as the run that
// queued it selected it: the fields of its daemon selector.
type QueuedMachine struct {
	ID     string            `json:"id,omitempty"`
	Name   string            `json:"name,omitempty"`
	Type   string            `json:"type,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// PausedForMachineChat is a chat whose root run the daemon-offline circuit
// breaker paused (chats.paused_for_machine_at) and nothing has resumed.
type PausedForMachineChat struct {
	ChatID string
	UserID string
	// WorkflowID is the paused root run.
	WorkflowID string
	// ActiveDaemonID is the machine the chat is pinned to; nil means the
	// user's default machine.
	ActiveDaemonID *string
	PausedAt       time.Time
}

// WaitingForMachineRun is a chat's live root run parked waiting for its
// machine.
type WaitingForMachineRun struct {
	ChatID     string
	WorkflowID string
}

// ChatActivityState is what the chats_with_activity view derives for one chat
// that the activity stream carries: its folded activity (a reliantv1.ChatActivity
// value) and how many of its threads are working (Chat.WorkingThreads).
type ChatActivityState struct {
	Activity       int
	WorkingThreads int
}
