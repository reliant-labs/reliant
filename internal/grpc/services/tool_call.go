// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

type ToolCallService struct {
	reliantv1connect.UnimplementedToolCallServiceHandler
	database   db.Repository
	tempClient client.Client
	router     toolexec.DaemonRouter
}

// NewToolCallService creates a new ToolCallService
func NewToolCallService(database db.Repository, tempClient client.Client, router toolexec.DaemonRouter) *ToolCallService {
	return &ToolCallService{
		database:   database,
		tempClient: tempClient,
		router:     router,
	}
}

// CancelToolCall cancels an executing tool call by setting an in-memory cancel signal and cancelling the Temporal workflow.
func (s *ToolCallService) CancelToolCall(
	ctx context.Context,
	req *connect.Request[reliantv1.CancelToolCallRequest],
) (*connect.Response[reliantv1.CancelToolCallResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("not authenticated"))
	}

	toolCallID := req.Msg.ToolCallId
	if toolCallID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("tool_call_id is required"))
	}

	// Find the content block by tool_call_id
	block, err := s.database.GetContentBlockByToolCallID(ctx, toolCallID)
	if err != nil {
		logging.Error("[CancelToolCall] Failed to find tool call", "error", err, "toolCallID", toolCallID)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tool call not found: %s", toolCallID))
	}

	// Get the message to find the chat ID
	msg, err := s.database.GetMessage(ctx, block.MessageID)
	if err != nil {
		logging.Error("[CancelToolCall] Failed to find message for tool call", "error", err, "messageID", block.MessageID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to find message for tool call"))
	}

	chatID := msg.ChatID

	// Get the chat and verify ownership
	chat, err := s.database.GetChat(ctx, chatID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Set the in-memory signal for immediate detection by running tool
	// This prevents the tool from emitting "completed" status after user clicked cancel
	shell.GetCancelSignal().SetCancelled(toolCallID)

	// Ask the daemon to stop this one execution.
	//
	// This used to cancel the chat's whole Temporal workflow. That is the
	// wrong blast radius: every tool call in an LLM turn executes as parallel
	// goroutines inside a SINGLE ExecuteTools activity, sharing one context.
	// Cancelling the workflow cancels that shared context, so every sibling
	// tool saw ctx.Err() != nil and reported itself cancelled -- including
	// ones that had already finished successfully. Cancelling one tool must
	// not decide the outcome of the others.
	//
	// The daemon registers a per-execution cancel func under the tool call id,
	// so this reaches exactly the one running tool. Best-effort by design: if
	// the daemon is offline or the tool runs server-side, the in-memory cancel
	// signal above is still authoritative -- the tool runs to completion and
	// execute_tools discards its result rather than reporting it as completed.
	if s.router != nil {
		if err := s.router.SendToolExecutionCancel(ctx, userID, toolCallID, "user cancelled tool call"); err != nil {
			logging.Warn("[CancelToolCall] Could not deliver cancel to daemon; relying on cancel signal",
				"error", err, "toolCallID", toolCallID)
		}
	}

	// A spawn does not run where any of the above can reach it. If that
	// delivery fails the spawn is still running, so report it rather than
	// marking the call cancelled — a cancel that says it worked and didn't is
	// worse than one that admits it couldn't.
	if err := s.cancelChildWorkflowForToolCall(ctx, toolCallID); err != nil {
		logging.Warn("[CancelToolCall] Could not deliver spawn cancellation",
			"toolCallID", toolCallID, "error", err)
		return nil, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("could not cancel this spawn: %w", err))
	}

	// Emit a tool_call cancelled status update for the UI, and record the same
	// transition durably so it survives a reload.
	s.emitToolCallCancelled(ctx, chatID, toolCallID, getToolName(block))
	s.persistToolCallStatus(ctx, chatID, toolCallID, block, msg, core.ToolCallStatusCancelled, "")

	sessionID := ""
	if chat.WorkflowID != nil {
		sessionID = *chat.WorkflowID
	}

	return connect.NewResponse(&reliantv1.CancelToolCallResponse{
		Success:    true,
		Message:    "Tool call cancellation requested",
		ToolCallId: toolCallID,
		SessionId:  sessionID,
		ChatId:     chatID,
	}), nil
}

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
// Terminate rather than Cancel: CancelWorkflow is cooperative, and a run parked
// on a signal Await never observes it and stays RUNNING forever. This is the
// same forceful stop CancelChat and the reconciler use.
//
// Best-effort throughout — a cancel must not fail because bookkeeping did.
func (s *ToolCallService) cancelChildWorkflowForToolCall(ctx context.Context, toolCallID string) error {
	calls, err := s.database.ListToolCallsByIDs(ctx, []string{toolCallID})
	if err != nil {
		logging.Warn("[CancelToolCall] Could not read tool call; cannot check for a child workflow",
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

	// Signal the PARENT workflow. A spawn is not a Temporal execution of its
	// own — executeSpawnInline runs it as a goroutine inside the parent — so
	// there is nothing here to terminate. Terminating child_workflow_id was the
	// previous implementation and always failed with "workflow not found for
	// ID": that id names a thread and a DB row, not a Temporal execution. The
	// failure was best-effort, so the row was still marked cancelled and the UI
	// reported success while the spawn ran on for another seventeen minutes.
	if s.tempClient == nil {
		return fmt.Errorf("no temporal client; cannot deliver cancellation for %s", toolCallID)
	}
	parentWorkflowID := call.ChatID
	if chat, err := s.database.GetChat(ctx, call.ChatID); err == nil && chat.WorkflowID != nil && *chat.WorkflowID != "" {
		parentWorkflowID = *chat.WorkflowID
	}
	if err := s.tempClient.SignalWorkflow(ctx, parentWorkflowID, "", v2.CancelThreadSignalName, v2.CancelThreadSignal{
		Thread:     childWorkflowID,
		ToolCallID: toolCallID,
	}); err != nil {
		return fmt.Errorf("failed to signal spawn cancellation to %s: %w", parentWorkflowID, err)
	}
	logging.Info("[CancelToolCall] Signalled spawn cancellation",
		"toolCallID", toolCallID, "childThread", childWorkflowID, "parentWorkflowID", parentWorkflowID)

	// Reconcile the workflow row. CAS rather than a blind write so a child that
	// settled terminally on its own is never clobbered; cover PAUSED too, since
	// a user cancel overrides a pause.
	for _, from := range []db.WorkflowStatus{db.WorkflowStatusRunning, db.WorkflowStatusPaused} {
		swapped, err := s.database.CompareAndSwapWorkflowStatus(ctx, childWorkflowID, db.WorkflowStatusCancelled, from)
		if err != nil {
			logging.Warn("[CancelToolCall] Failed to reconcile spawned workflow status",
				"childWorkflowID", childWorkflowID, "from", from, "error", err)
			return nil
		}
		if swapped {
			return nil
		}
	}
	return nil
}

// ConvertToBackground converts an executing tool call to a background process
// This allows the tool to continue running while the workflow can proceed
func (s *ToolCallService) ConvertToBackground(
	ctx context.Context,
	req *connect.Request[reliantv1.ConvertToBackgroundRequest],
) (*connect.Response[reliantv1.ConvertToBackgroundResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("not authenticated"))
	}

	toolCallID := req.Msg.ToolCallId
	if toolCallID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("tool_call_id is required"))
	}

	// Find the content block by tool_call_id
	block, err := s.database.GetContentBlockByToolCallID(ctx, toolCallID)
	if err != nil {
		logging.Error("[ConvertToBackground] Failed to find tool call", "error", err, "toolCallID", toolCallID)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tool call not found: %s", toolCallID))
	}

	// Get the message to find the chat ID
	msg, err := s.database.GetMessage(ctx, block.MessageID)
	if err != nil {
		logging.Error("[ConvertToBackground] Failed to find message for tool call", "error", err, "messageID", block.MessageID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to find message for tool call"))
	}

	chatID := msg.ChatID

	// Get the chat and verify ownership
	chat, err := s.database.GetChat(ctx, chatID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// First, check if there's already a background process for this tool call
	bgManager := shell.GetBackgroundManager()
	processes := bgManager.GetProcessesByChat(chatID)
	for _, p := range processes {
		if p.Status == "running" {
			// The call is already backgrounded — record that durably with the
			// process id this response reports. The id is the chat's running
			// process rather than one matched to this tool call; that
			// attribution is what the API has always returned here.
			s.persistToolCallStatus(ctx, chatID, toolCallID, block, msg, core.ToolCallStatusBackgrounded, p.ID)

			sessionID := ""
			if chat.WorkflowID != nil {
				sessionID = *chat.WorkflowID
			}

			return connect.NewResponse(&reliantv1.ConvertToBackgroundResponse{
				Success:    true,
				Message:    "Tool execution is already running as background process",
				ProcessId:  p.ID,
				ToolCallId: toolCallID,
				SessionId:  sessionID,
				ChatId:     chatID,
			}), nil
		}
	}

	// The command runs in the DAEMON — a separate process, possibly on another
	// machine — so the request has to cross the wire. The in-memory signal
	// below is kept only for an in-process executor; on its own it does
	// nothing, which is exactly what this endpoint used to do: mark the row
	// BACKGROUNDED, report success, and leave the command running in the
	// foreground with a NULL background_process_id.
	shell.GetBackgroundSignal().SetBackgrounded(toolCallID)

	if s.router == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("cannot background: no daemon router configured"))
	}
	// Addressed by tool call id: that is the only id the UI can name, and the
	// daemon registers each execution under it (see runtime.go).
	if err := s.router.SendToolExecutionBackground(ctx, userID, toolCallID, toolCallID); err != nil {
		// Do not claim success. The command is still running in the foreground,
		// and reporting otherwise is what made this look like a working feature
		// for months.
		logging.Warn("[ConvertToBackground] Could not deliver background request to daemon",
			"error", err, "toolCallID", toolCallID)
		return nil, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("could not reach the machine running this command: %w", err))
	}

	// Emit a tool_call backgrounded status update for the UI, and record the
	// same transition durably. The real process id arrives with the tool
	// result once the daemon has adopted the process.
	s.emitToolCallBackgrounded(ctx, chatID, toolCallID, getToolName(block))
	s.persistToolCallStatus(ctx, chatID, toolCallID, block, msg, core.ToolCallStatusBackgrounded, "")

	sessionID := ""
	if chat.WorkflowID != nil {
		sessionID = *chat.WorkflowID
	}

	// Return success - the tool executor will handle the actual background conversion
	return connect.NewResponse(&reliantv1.ConvertToBackgroundResponse{
		Success:    true,
		Message:    "Background conversion requested - tool will be converted to background process",
		ProcessId:  "", // Process ID will be assigned when the tool converts
		ToolCallId: toolCallID,
		SessionId:  sessionID,
		ChatId:     chatID,
	}), nil
}

// emitToolCallCancelled emits a tool_call cancelled status update to chat_updates.
// Keyed by the LLM tool-call id, matching every other tool-status emitter — the
// block UUID this handler also has on hand is a different identifier space and
// no status consumer looks it up.
func (s *ToolCallService) emitToolCallCancelled(ctx context.Context, chatID, toolCallID, toolName string) {
	update := db.ToolCallUpdate{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Status:     db.ToolCallStatusCancelled,
	}
	if err := s.database.EmitToolCallCancelledUpdate(ctx, chatID, update); err != nil {
		logging.Error("[CancelToolCall] Failed to create chat update", "error", err)
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
func (s *ToolCallService) persistToolCallStatus(
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

	if err := db.UpsertToolCallStatus(ctx, s.database, call); err != nil {
		logging.Error("[ToolCallService] Failed to persist tool call status",
			"error", err, "toolCallID", toolCallID, "status", status)
	}
}

// emitToolCallBackgrounded emits a tool_call backgrounded status update to chat_updates
func (s *ToolCallService) emitToolCallBackgrounded(ctx context.Context, chatID, toolCallID, toolName string) {
	update := db.ToolCallUpdate{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Status:     db.ToolCallStatusBackgrounded,
	}
	if err := s.database.EmitToolCallBackgroundedUpdate(ctx, chatID, update); err != nil {
		logging.Error("[ConvertToBackground] Failed to create chat update", "error", err)
	}
}

// getToolName extracts tool name from content block
func getToolName(block *db.MessageContentBlock) string {
	if block.ToolName != nil {
		return *block.ToolName
	}
	return "unknown"
}
