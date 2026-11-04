// This is the persistence layer: the exported surface is concrete data types
// and their store methods, consumed through the interfaces the calling
// services declare for themselves (the narrow-consumer-interface pattern, as
// in internal/runs/contract.go). A contract.go here would be one wide
// interface over every query, which no caller consumes.
//
//forge:exclude-contract: domain entity types and the per-aggregate store interfaces consumed across the app; type vocabulary
package core

import (
	"errors"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// AgentMessageKind identifies what an agent_messages row carries. It is the
// wire enum itself (reliantv1.AgentMessageKind), so the mailbox column, the
// messages.sender_kind column and the API cannot drift apart. Values are
// fixed: changing one silently reinterprets every row already in the database.
type AgentMessageKind = reliantv1.AgentMessageKind

const (
	// AgentMessageKindUnspecified is the zero value; a persisted row should
	// never carry it.
	AgentMessageKindUnspecified = reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_UNSPECIFIED
	// AgentMessageKindMessage is free-form text sent between a parent and a
	// running sub-agent (spawn_send).
	AgentMessageKindMessage = reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_MESSAGE
	// AgentMessageKindCompletion notifies the parent that a sub-agent
	// finished successfully.
	AgentMessageKindCompletion = reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_COMPLETION
	// AgentMessageKindCancelled notifies the parent that a sub-agent was
	// cancelled.
	AgentMessageKindCancelled = reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_CANCELLED
	// AgentMessageKindFailed notifies the parent that a sub-agent failed.
	AgentMessageKindFailed = reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_FAILED
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
	AgentMessageKindHumanMessage = reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_HUMAN_MESSAGE
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
	// Subject is the one-line summary an agent gives a message it sends
	// (spawn_send's `subject`); the transcript shows the message collapsed to
	// it. Empty for every kind that carries none.
	Subject string
	Body    string
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
	// Synthesized marks a terminal report (Completion/Cancelled/Failed) the
	// reconciler fabricated because the real one never arrived. A real report
	// for the same ToolCallID supersedes it (EnqueueSpawnReport); a real report
	// is never overwritten. The column is the contract -- never infer this from
	// Body. See dev-docs/incidents/2026-10-04-spawn-report-collision.md.
	Synthesized bool
}

// SpawnReportOutcome is what EnqueueSpawnReport did with a real terminal report.
type SpawnReportOutcome int

const (
	// SpawnReportInserted: no report existed for the spawn; the row was written.
	SpawnReportInserted SpawnReportOutcome = iota + 1
	// SpawnReportSuperseded: a reconciler-synthesized placeholder was replaced
	// and re-queued, even if it had already been delivered, so the parent
	// receives the actual outcome.
	SpawnReportSuperseded
	// SpawnReportAlreadyReported: this spawn's real report already existed
	// (a retry); nothing changed.
	SpawnReportAlreadyReported
)

// ErrSpawnReportSlotTaken is returned when a terminal report is written for a
// spawn whose slot -- (ChatID, ToolCallID) -- already holds the report of a
// DIFFERENT spawn: another child, or another parent, under the same id.
//
// The tool call id is the model provider's, so a provider that repeats ids
// can give two spawns in one chat the same one. Only one report fits the
// slot. The second is an error rather than "already reported": treating it as
// a retry is exactly how a sub-agent's result used to disappear without a
// trace.
var ErrSpawnReportSlotTaken = errors.New("another spawn already reported under this tool call id")
