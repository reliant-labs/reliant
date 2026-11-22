package handlers

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// interruptedTurnReason is the error message recorded on a call settled
// because the user interrupted its turn.
const interruptedTurnReason = "user interrupted the agent"

// settleInterruptedCall answers one call of a batch the user interrupted
// (RuntimeContext.InterruptedTurn) without running it.
//
// The runtime dispatches such a batch once, after the interrupt cancelled the
// dispatch that was running it (runtime/interrupt_settle.go), so every call of
// the turn gets a tool_result and the loop reaches the call_llm that delivers
// the user's message. What each call gets is decided by its durable row:
//
//   - a terminal row (it finished, failed, or the API's InterruptThread
//     recorded it Cancelled) keeps that recorded outcome;
//   - a Backgrounded row is a process still running elsewhere, which owes its
//     own outcome later: its recorded result is returned and the row is left
//     alone, exactly as the reconciler expects;
//   - a Pending or Executing row may have started, so its effects are
//     unknown: it is recorded Cancelled with the result that says so;
//   - no row means the call never started (it was still waiting on its
//     activation preview or approval card, or the batch was cancelled before
//     dispatching it): it is recorded Cancelled as not run;
//   - a row that cannot be read (the database failed the lookup) proves
//     neither: the call may have started, so its result says the effects are
//     unknown, and nothing is written over a row this cannot see.
//
// A row that belongs to another chat or thread (a provider-chosen id that
// repeats) is not this call's, so it is neither returned nor overwritten.
func (a *ExecuteToolsActivity) settleInterruptedCall(ctx context.Context, rtx RuntimeContext, call message.ToolCall) message.ToolResult {
	if result, ok := a.checkPriorTerminalResult(ctx, rtx.ChatID, rtx.Thread, call.ID, call.Name); ok {
		return result
	}

	content := NotRunInterruptedToolResultContent
	existing, err := a.toolCalls.FindToolCall(ctx, call.ID)
	if err != nil && !svcerr.IsNotFound(err) {
		activity.GetLogger(ctx).Warn("[ExecuteTools] Interrupted turn: could not read the call's row; answering it as of unknown effect",
			"tool_call_id", call.ID,
			"tool_name", call.Name,
			"error", err)
		return a.buildToolResult(call.ID, call.Name, CancelledByInterruptToolResultContent, "", true, nil, nil)
	}
	if err == nil && existing != nil {
		if !toolCallIsThisThreads(existing, rtx.ChatID, rtx.Thread) {
			return a.buildToolResult(call.ID, call.Name, content, "", true, nil, nil)
		}
		if existing.Status == core.ToolCallStatusBackgrounded {
			if recorded, err := a.toolCalls.FindToolCallResult(ctx, call.ID); err == nil && recorded != nil {
				return a.buildToolResult(call.ID, call.Name, recorded.Content, "", recorded.IsError, nil, nil)
			}
			return a.buildToolResult(call.ID, call.Name, InterruptedToolResultContent, "", true, nil, nil)
		}
		content = CancelledByInterruptToolResultContent
	}

	activity.GetLogger(ctx).Info("[ExecuteTools] Interrupted turn: answering the call without running it",
		"tool_call_id", call.ID,
		"tool_name", call.Name,
		"had_row", existing != nil)
	completedAt := time.Now()
	a.upsertTerminalToolCall(ctx, &toolExecutionContext{
		chatID:     rtx.ChatID,
		thread:     rtx.Thread,
		toolName:   call.Name,
		toolInput:  call.Input,
		toolCallID: call.ID,
	}, core.ToolCallStatusCancelled, toolCallUpsertOpts{
		completedAt:  &completedAt,
		errorMessage: interruptedTurnReason,
	}, &toolCallResultWrite{content: content, isError: true})
	a.emitToolStatus(ctx, rtx.ChatID, call.ID, call.Name, toolStatusEvent(core.ToolCallStatusCancelled))
	return a.buildToolResult(call.ID, call.Name, content, "", true, nil, nil)
}
