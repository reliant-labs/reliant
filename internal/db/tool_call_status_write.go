// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// UpsertToolCallStatus records a tool call status transition without losing
// what an earlier transition already established.
//
// UpsertToolCall is a full-row upsert: its ON CONFLICT clause assigns every
// mutable column from the incoming row. That is the right primitive for a
// writer that knows the whole call, but tool status is written from three
// independent places -- the execute_tools activity, the EmitToolCallStatus
// activity on the spawn path, and the CancelToolCall/ConvertToBackground
// handlers -- and each knows only part of it. A handler cancelling a call
// knows nothing about the started_at the activity recorded, so a plain upsert
// from there would write NULL over it and the row would claim a call that ran
// never started.
//
// So: read the existing row and let it fill in whatever the caller left unset.
// The caller's values always win where it supplied one; only zero/nil fields
// inherit. A missing or unreadable existing row is not an error -- this is the
// first write for that call, and the caller's row stands on its own.
//
// The read/write pair is not atomic, but status transitions for a single tool
// call are sequential (requested, then executing, then terminal), and every
// write here is best-effort status bookkeeping that must never fail the tool
// call it describes. A lost update between two concurrent writers costs one
// column of provenance, not correctness of the status itself.
//
// Terminal is a one-way door. A call that has completed, failed or been
// cancelled has its outcome; a later writer that still believes the call is
// running (a racing status emitter, a retried activity) must not walk the row
// back to EXECUTING and strand the UI on a spinner for a call that is over.
// The non-terminal write is dropped rather than erroring: it is bookkeeping
// about a transition that has been overtaken by events, not a failure.
//
// The FIRST terminal write wins, too: see pinTerminalOutcome.
//
// The existing row must be THIS chat's. A tool call id is the model
// provider's, and a provider can hand two chats the same one; inheriting from
// another chat's row is how a call used to acquire that chat's thread and
// message, and the terminal door above would have let another chat's finished
// call decide this one's status. Such a write is refused with
// core.ErrToolCallIDInAnotherChat, and UpsertToolCall refuses it again at the
// database, where no concurrent first write can slip past the read.
func UpsertToolCallStatus(ctx context.Context, repo Repository, call *core.ToolCall) error {
	if call == nil {
		return nil
	}

	if existing, err := repo.GetToolCall(ctx, call.ID); err == nil && existing != nil {
		if existing.ChatID != call.ChatID {
			logging.Warn("[ToolCallStatus] Tool call id is already used by another chat's call; not recording this chat's status",
				"tool_call_id", call.ID, "chat_id", call.ChatID, "holding_chat_id", existing.ChatID, "status", call.Status)
			return fmt.Errorf("%w: id %q, writing for chat %q", core.ErrToolCallIDInAnotherChat, call.ID, call.ChatID)
		}
		if existing.Status.IsTerminal() {
			if !call.Status.IsTerminal() {
				return nil
			}
			pinTerminalOutcome(existing, call)
		}
		inheritToolCallFields(existing, call)
	}

	resolveToolCallMessage(ctx, repo, call)

	return repo.UpsertToolCall(ctx, call)
}

// pinTerminalOutcome keeps a terminal call's outcome -- status, completed_at
// and error message -- as its first terminal write recorded it, while still
// letting a later write fill in columns that write did not know (message,
// thread, child workflow).
//
// A call ends once. Every later terminal write comes from a writer that was
// not there when it ended and is describing it from the outside:
//   - Cleanup's orphan sweep closing a row that was already closed, which
//     moved completed_at on 3 rows in 15 days of the dev stack's data;
//   - a cancel aimed at a sibling tool (every call in a turn shares one
//     context), which used to relabel a COMPLETED call cancelled and erase a
//     result the user had already seen;
//   - an abandoned activity attempt finishing after Temporal re-dispatched it
//     (runtime.shieldFromSpuriousHeartbeatCancel lets a tool outlive a failed
//     heartbeat RPC). The re-dispatch already reported the call interrupted
//     and closed the row; the conversation holds that answer, so the late
//     COMPLETED must not make the durable record disagree with it.
//
// The earlier rule let one terminal status "correct" another (a cancel after a
// failure). None of the writers above is a correction, and no writer is known
// that is, so the rule is the simple one: first terminal write wins.
func pinTerminalOutcome(existing, call *core.ToolCall) {
	if call.Status != existing.Status {
		logging.Info("[ToolCallStatus] Tool call already terminal; keeping its first outcome",
			"tool_call_id", call.ID, "status", existing.Status, "ignored_status", call.Status)
	}
	call.Status = existing.Status
	call.CompletedAt = existing.CompletedAt
	call.ErrorMessage = existing.ErrorMessage
}

// resolveToolCallMessage fills in message_id (and thread_id) from the call's
// own content block when the writer did not supply them.
//
// Only one of the writers that reach this function has a *db.Message in hand
// (the CancelToolCall / ConvertToBackground handlers). The two that run during
// a live tool call -- the execute_tools activity and the EmitToolCallStatus
// activity on the spawn path -- are workflow-side and never had one: a spawn's
// config carries toolCallID, childWorkflowID, prompt and preset, and the proto
// ToolCallMsg it is parsed from is only {id, name, input}. So the rows those
// writers create have message_id NULL.
//
// That NULL is not cosmetic. Every read path enriches tool-call blocks via
// ListToolCallsByMessageIDs, whose predicate is `WHERE message_id IN (...)` --
// a NULL row can never match it. The block therefore shipped without the
// child_workflow_id that names the thread a spawn owns, and the spawn's
// preview had nothing to render for the entire time it was running. It filled
// in on reload only because a later, message-aware write had populated the
// column by then.
//
// The link is recoverable because the ordering guarantees it: the assistant
// message and its tool_call block are persisted BEFORE tools execute. Measured
// on live data, the block predates its tool_calls row by 0.11-0.30s in every
// case, and all 124 historical rows with a NULL message_id have a block that
// carries one. So this is a lookup, not a guess.
//
// Best-effort, like everything else on this path: a tool call must never fail
// because its bookkeeping could not find a message.
func resolveToolCallMessage(ctx context.Context, repo Repository, call *core.ToolCall) {
	if call.MessageID != nil && *call.MessageID != "" {
		return
	}
	if call.ID == "" {
		return
	}

	block, err := repo.GetContentBlockByToolCallID(ctx, call.ID)
	if err != nil || block == nil || block.MessageID == "" {
		return
	}
	// The lookup is by tool call id alone, so the block can be another chat's
	// call under the same provider-chosen id. Link only a message in this
	// call's chat: a call pointing at another chat's message is shown in that
	// chat's transcript.
	msg, err := repo.GetMessage(ctx, block.MessageID)
	if err != nil || msg == nil || msg.ChatID != call.ChatID {
		return
	}
	call.MessageID = &block.MessageID

	// The message's thread is the call's thread. Same reasoning: the writers
	// that lack a message usually lack the thread too.
	if (call.ThreadID == nil || *call.ThreadID == "") && msg.ThreadID != "" {
		call.ThreadID = &msg.ThreadID
	}
}

// inheritToolCallFields copies fields from the persisted row into the incoming
// one wherever the caller left them unset.
func inheritToolCallFields(existing, call *core.ToolCall) {
	// requested_at and created_at are established by the first write and are
	// not part of the upsert's DO UPDATE set, but carrying them forward keeps
	// the in-memory row honest for anything that reads it back.
	if !existing.RequestedAt.IsZero() {
		call.RequestedAt = existing.RequestedAt
	}
	if !existing.CreatedAt.IsZero() {
		call.CreatedAt = existing.CreatedAt
	}
	if call.ToolName == "" {
		call.ToolName = existing.ToolName
	}
	if call.Input == nil {
		call.Input = existing.Input
	}
	if call.ThreadID == nil {
		call.ThreadID = existing.ThreadID
	}
	if call.MessageID == nil {
		call.MessageID = existing.MessageID
	}
	if call.StartedAt == nil {
		call.StartedAt = existing.StartedAt
	}
	if call.CompletedAt == nil {
		call.CompletedAt = existing.CompletedAt
	}
	if call.ErrorMessage == nil {
		call.ErrorMessage = existing.ErrorMessage
	}
	if call.ChildWorkflowID == nil {
		call.ChildWorkflowID = existing.ChildWorkflowID
	}
	if call.BackgroundProcessID == nil {
		call.BackgroundProcessID = existing.BackgroundProcessID
	}
	if call.DaemonID == nil {
		call.DaemonID = existing.DaemonID
	}
}
