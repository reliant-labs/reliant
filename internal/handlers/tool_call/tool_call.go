// Copyright (c) 2025 Reliant Labs
package tool_call

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
)

// cancelChildWorkflowForToolCall stops the workflow a spawn tool call started.
//
// Every other mechanism in CancelToolCall targets a tool running inside this
// process or inside the daemon: an in-memory cancel signal keyed by tool call
// id, and a per-execution cancel func the daemon registers. A spawn is neither.
// Its work is a separate Temporal workflow with its own lifecycle, in its own
// worker, so the signal is not in its process and the daemon is not running it.
//
// Without this, cancelling a spawn wrote status=CANCELLED on the tool_calls row
// and changed nothing else. Observed on a real chat: the row went CANCELLED at
// 19:38:29 while child workflow 216e52af stayed RUNNING and wrote four more
// messages over the next three minutes, having already run for over an hour.
// The user cancelled, watched it keep working, and typed "can you stop".
//
// child_workflow_id is the link, and it is already stored on the row — the
// spawn path writes it when it creates the child. Nothing had to be derived.
//
// Blast radius is exactly the spawned child. This deliberately does NOT cancel
// the chat's workflow: sibling tool calls in the same LLM turn execute as
// parallel goroutines sharing one context inside a single ExecuteTools
// activity, and cancelling that context made every sibling report itself
// cancelled — including ones that had already succeeded. That is why cancel was
// narrowed to the daemon signal in the first place; the narrowing simply left
// spawns with no path at all.
//
// The stop itself is NOT best-effort: an undelivered signal means the spawn is
// still running, so that error propagates and CancelToolCall reports
// Unavailable rather than marking the call cancelled. Only the bookkeeping
// behind it is best-effort — see temporal.SpawnStopper, which this delegates
// to and which the spawn_stop tool shares.
func (s *Service) cancelChildWorkflowForToolCall(ctx context.Context, toolCallID string) error {
	calls, err := s.deps.Repo.ListToolCallsByIDs(ctx, []string{toolCallID})
	if err != nil {
		logging.WarnContext(ctx, "[CancelToolCall] Could not read tool call; cannot check for a child workflow",
			"toolCallID", toolCallID, "error", err)
		return nil
	}
	if len(calls) == 0 {
		return nil
	}
	call := calls[0]
	if call.ChildWorkflowID == nil || *call.ChildWorkflowID == "" {
		return nil // not a spawn; the mechanisms above are the whole story
	}
	childWorkflowID := *call.ChildWorkflowID

	// Signal the ROOT workflow, then reconcile the spawn's row — both of which
	// temporal.SpawnStopper does, and does identically for the spawn_stop tool.
	// The two callers shared these steps verbatim before this was extracted,
	// and a second copy of "which signal, to which workflow id, with which ids
	// in the payload" is exactly the drift that produced the phantom cancel
	// described above.
	if s.deps.Temporal == nil {
		return fmt.Errorf("no temporal client; cannot deliver cancellation for %s", toolCallID)
	}
	// child_workflow_id names the workflow row for THIS call, which is the row
	// to reconcile — but it is NOT the child's thread for a resumed spawn,
	// where the thread is the original one and this id is derived from the new
	// tool call. The workflow row's own `thread` column is the authoritative
	// link (CreateWorkflowWithThread always sets it, new or resumed), so read
	// it rather than assuming the two ids coincide.
	childThreadID := childWorkflowID
	if wf, wfErr := s.deps.Repo.GetWorkflow(ctx, childWorkflowID); wfErr == nil && wf != nil && wf.Thread != "" {
		childThreadID = wf.Thread
	}

	stopper := rtemporal.NewSpawnStopper(s.deps.Temporal, chatWorkflowIDLookup(s.deps.Chats), s.deps.Repo)
	return stopper.StopSpawn(ctx, call.ChatID, rtemporal.SpawnRef{
		ThreadID:   childThreadID,
		WorkflowID: childWorkflowID,
		ToolCallID: toolCallID,
	})
}

// chatReader reads one chat's own row; Chats satisfies it.
type chatReader interface {
	GetChatRow(ctx context.Context, id string) (*core.ChatRow, error)
}

// chatWorkflowIDLookup resolves a chat id to the Temporal workflow id driving
// it, for the spawn stopper.
//
// Duplicated from workersetup.ChatWorkflowLookup rather than imported: that
// package pulls in the worker's whole activity registration, which this
// service has no business depending on for one field read. A chat's workflow
// is normally created with the chat id as its identity, but chats.workflow_id
// is authoritative — a run restarted after the history limit carries a
// different id, and signalling the chat id would silently reach nothing.
func chatWorkflowIDLookup(repo chatReader) func(ctx context.Context, chatID string) (string, bool) {
	return func(ctx context.Context, chatID string) (string, bool) {
		if repo == nil {
			return "", false
		}
		chat, err := repo.GetChatRow(ctx, chatID)
		if err != nil || chat == nil || chat.WorkflowID == nil || *chat.WorkflowID == "" {
			return "", false
		}
		return *chat.WorkflowID, true
	}
}

// persistToolCallStatus writes the durable tool_calls row for a status change
// driven by the user (cancel / send-to-background), complementing the
// chat_updates event the sibling emitters send.
//
// Best-effort by the same rule as those emitters: a failed write is logged and
// the RPC still succeeds. Cancelling a tool call must not fail because its
// bookkeeping did.
//
// Unlike the execution paths, this handler already looked up the block and its
// message to get here, so thread_id / message_id / input are free and worth
// recording.
func (s *Service) persistToolCallStatus(
	ctx context.Context,
	chatID, toolCallID string,
	block *db.MessageContentBlock,
	msg *db.Message,
	status core.ToolCallStatus,
	backgroundProcessID string,
) {
	now := time.Now()
	call := &core.ToolCall{
		ID:          toolCallID,
		ChatID:      chatID,
		ToolName:    getToolName(block),
		Status:      status,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if msg != nil {
		call.MessageID = &msg.ID
		if msg.ThreadID != "" {
			call.ThreadID = &msg.ThreadID
		}
	}
	if block != nil && block.ToolInput != nil && json.Valid([]byte(*block.ToolInput)) {
		call.Input = []byte(*block.ToolInput)
	}
	// A cancelled call is terminal: it will never produce a result, so it has
	// a completion time. A backgrounded one is still running elsewhere.
	if status == core.ToolCallStatusCancelled {
		call.CompletedAt = &now
	}
	if backgroundProcessID != "" {
		call.BackgroundProcessID = &backgroundProcessID
	}

	if err := db.UpsertToolCallStatus(ctx, s.deps.Repo, call); err != nil {
		logging.ErrorContext(ctx, "[ToolCallService] Failed to persist tool call status",
			"error", err, "toolCallID", toolCallID, "status", status)
	}
}

// getToolName extracts tool name from content block
func getToolName(block *db.MessageContentBlock) string {
	if block.ToolName != nil {
		return *block.ToolName
	}
	return "unknown"
}
