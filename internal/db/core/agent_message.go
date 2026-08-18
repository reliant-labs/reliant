package core

import (
	"context"
	"time"
)

// AgentMessageKind identifies what an agent_messages row carries.
//
// These values are the wire contract for the agent_messages.kind column.
// Values are fixed: changing one silently reinterprets every row already in
// the database.
type AgentMessageKind int32

const (
	// AgentMessageKindUnspecified is the zero value; a persisted row should
	// never carry it.
	AgentMessageKindUnspecified AgentMessageKind = 0
	// AgentMessageKindMessage is free-form text sent between a parent and a
	// running sub-agent (spawn_send).
	AgentMessageKindMessage AgentMessageKind = 1
	// AgentMessageKindCompletion notifies the parent that a sub-agent
	// finished successfully.
	AgentMessageKindCompletion AgentMessageKind = 2
	// AgentMessageKindCancelled notifies the parent that a sub-agent was
	// cancelled.
	AgentMessageKindCancelled AgentMessageKind = 3
	// AgentMessageKindFailed notifies the parent that a sub-agent failed.
	AgentMessageKindFailed AgentMessageKind = 4
	// AgentMessageKindHumanMessage is free-form text sent by the HUMAN user
	// directly into a running thread (the SendAgentMessage RPC), as opposed
	// to AgentMessageKindMessage which is agent-to-agent (spawn_send).
	//
	// This needs its own kind rather than reusing AgentMessageKindMessage
	// with a human-looking FromThreadID: FromThreadID must reference a real
	// threads row (FK), and the chat's own root thread is not reliably "the
	// human" -- a root AGENT can also spawn_send into one of its own
	// children, which would collide on the same FromThreadID. Kind is what
	// the drain envelope actually keys off to label the sender, so it must
	// be unambiguous on its own.
	AgentMessageKindHumanMessage AgentMessageKind = 5
)

// AgentMessageStatus is the durable lifecycle state of a mailbox row.
type AgentMessageStatus int32

const (
	// AgentMessageStatusUnspecified is the zero value; a persisted row
	// should never carry it.
	AgentMessageStatusUnspecified AgentMessageStatus = 0
	// AgentMessageStatusQueued means the message has not yet been drained
	// into the recipient thread's history.
	AgentMessageStatusQueued AgentMessageStatus = 1
	// AgentMessageStatusDelivered means the message was folded into a
	// message on the recipient thread. A row in this state must have
	// DeliveredAt set (enforced by a CHECK constraint).
	AgentMessageStatusDelivered AgentMessageStatus = 2
	// AgentMessageStatusUndelivered means the recipient thread's loop
	// exited before this row could be drained, so it never will be.
	//
	// Delivery only happens in CallLLM, which drains the thread's mailbox
	// before it reads history. A message queued while a thread is
	// genuinely running, whose loop then exits before reaching another
	// CallLLM, is undeliverable by construction -- an inherent race that
	// no enqueue-time liveness check can close, because the thread WAS
	// live when the row was written. Observed on real data: two human
	// messages queued at 00:06:31 and 00:06:51 into a thread that
	// completed at 00:06:56, both left at queued with delivered_at NULL
	// indefinitely.
	//
	// This is a distinct value rather than a reuse of Delivered or a
	// DELETE, for two reasons. Marking delivered would be a lie the UI
	// would faithfully repeat back to a user whose words were never read.
	// Deleting would silently discard them, which is the current failure
	// mode with a tidier audit trail -- the row is the only remaining
	// record that the user said something, and it is what lets the UI
	// report "never delivered" honestly and offer to resend.
	//
	// Deliberately NOT subject to the delivered_at CHECK constraint
	// (status <> 2 OR delivered_at IS NOT NULL): an undelivered row has no
	// delivery time precisely because there was no delivery, and its NULL
	// delivered_at is the durable evidence of that.
	AgentMessageStatusUndelivered AgentMessageStatus = 3
)

// AgentMessage is a durable mailbox entry: one queued piece of communication
// from one thread to another (spawn_send, or a sub-agent's completion
// notification to its parent).
//
// A bare INSERT into `messages` cannot deliver this safely -- an agent is
// mid-turn most of the time, and inserting a user message between an
// assistant-with-tool_calls row and its tool_results row deadlocks the
// provider (see 20260811000000_add_agent_messages.sql). So delivery is
// queued here and only drained into `messages` at a boundary where history
// is known-consistent.
type AgentMessage struct {
	ID           string
	ChatID       string
	FromThreadID string
	ToThreadID   string
	Kind         AgentMessageKind
	Body         string
	// Attachments are attachment IDs, exactly as SaveMessageToThread takes
	// them -- a queued message carries files the same way a normally-sent
	// one does, and the drain fold hands this slice straight to
	// threads.Service.SaveMessage.
	Attachments []string
	// ToolCallID is the spawn call that owns the subject agent, when known.
	ToolCallID *string
	Status     AgentMessageStatus
	CreatedAt  time.Time
	// DeliveredAt and DeliveredMessageID are set together when the message
	// is drained into the recipient thread's history.
	DeliveredAt        *time.Time
	DeliveredMessageID *string
}

// AgentMessageStore is the shared contract for mailbox persistence across
// drivers.
type AgentMessageStore interface {
	EnqueueAgentMessage(ctx context.Context, msg *AgentMessage) error
	// EnqueueAgentMessageIfAbsent inserts msg (which must carry a terminal
	// Kind: Completion, Cancelled, or Failed) unless a terminal report for
	// the same ToolCallID already exists, enforced by a DB constraint so the
	// check-and-insert is atomic under concurrent callers (see
	// idx_agent_messages_one_terminal_report_per_spawn). Returns inserted =
	// true when this call's row landed, false when a report already existed
	// (the ordinary outcome the second of two racing callers sees, not an
	// error).
	EnqueueAgentMessageIfAbsent(ctx context.Context, msg *AgentMessage) (inserted bool, err error)
	// ListQueuedAgentMessagesForThread returns queued messages for a
	// recipient thread, ordered by created_at ascending -- delivery order
	// must match send order.
	ListQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) ([]*AgentMessage, error)
	// MarkAgentMessagesDelivered marks a batch of messages delivered,
	// recording when and into which message they landed, and returns the ids it
	// actually moved.
	//
	// Conditional on the rows still being queued, which is what makes a drain
	// idempotent: the drain reads its rows before it writes them, so two drains
	// racing on one thread can select the same batch. The loser moves nothing
	// and gets back fewer ids than it asked for -- its signal to abandon its own
	// inserts rather than write the same messages into the transcript twice.
	MarkAgentMessagesDelivered(ctx context.Context, ids []string, deliveredAt time.Time, deliveredMessageID string) ([]string, error)
	// SetAgentMessagesDeliveredMessageID backfills the envelope pointer on rows
	// the caller already claimed via MarkAgentMessagesDelivered.
	SetAgentMessagesDeliveredMessageID(ctx context.Context, ids []string, deliveredMessageID string) error
	CountQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) (int64, error)
	// CancelQueuedAgentMessage deletes a mailbox row identified by (id,
	// chatID) ONLY IF it is still queued -- this races against the target
	// agent's own drain loop, so the deletion is conditioned on status = 1
	// at the database level rather than checked-then-deleted in application
	// code. Returns cancelled = false, with the row left untouched, when
	// the message had already been delivered (or never existed / belonged
	// to a different chat).
	CancelQueuedAgentMessage(ctx context.Context, id, chatID string) (cancelled bool, err error)
	// ClaimQueuedAgentMessagesForThread atomically removes still-queued
	// HUMAN messages for a thread and returns exactly the rows it removed,
	// oldest first. Pass a non-empty messageID to claim a single message, or
	// "" to claim the whole queue.
	//
	// This is take-and-return in ONE statement, which is what makes "pull my
	// queued messages back and send them properly" raceless: a row the
	// agent's drain won first is simply absent from the result, so a caller
	// can never resend something the agent already received. Callers must
	// treat the returned slice — not their own prior view of the queue — as
	// the definitive list of what they now own.
	ClaimQueuedAgentMessagesForThread(ctx context.Context, toThreadID, chatID, messageID string) ([]*AgentMessage, error)
	// MarkQueuedAgentMessagesUndeliveredForThread resolves every still-queued
	// row addressed to a thread whose loop has exited, moving them to
	// AgentMessageStatusUndelivered, and returns how many it moved.
	//
	// Called when a thread reaches a terminal status: from that moment there
	// is no future loop boundary to drain into, so a row left at queued is
	// not "pending", it is stranded, and only a distinct status can say so.
	// Idempotent -- it matches queued rows only, so re-running it (a retried
	// activity, a reconciler pass racing the live path) moves nothing the
	// first call already moved.
	MarkQueuedAgentMessagesUndeliveredForThread(ctx context.Context, toThreadID string) (int64, error)
	// ListThreadsWithOrphanedAgentMessages returns the id of every thread
	// that is already terminal but still has queued rows addressed to it --
	// the backlog the live path above did not catch, either because it
	// predates that path or because the process died between the two writes.
	ListThreadsWithOrphanedAgentMessages(ctx context.Context) ([]string, error)
}
