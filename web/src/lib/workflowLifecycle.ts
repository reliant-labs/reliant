import {
  WorkflowState,
  WorkflowStopReason,
} from "../gen/reliant/v1/chat_pb";

/** Whether a stopped workflow is deliberately parked and expected to resume. */
export function isWorkflowPaused(
  state: WorkflowState,
  stopReason: WorkflowStopReason,
): boolean {
  return (
    state === WorkflowState.STOPPED &&
    stopReason === WorkflowStopReason.PAUSED
  );
}

/** Whether the chat's root run has not started: its first send must go through StartChat. */
export function isWorkflowPending(state: WorkflowState): boolean {
  return state === WorkflowState.PENDING;
}

/** Whether a send on this chat is its first and must call StartChat instead of SendMessage. */
export function chatNeedsStart(
  chat: { workflowState: WorkflowState } | null | undefined,
): boolean {
  return !!chat && isWorkflowPending(chat.workflowState);
}

/**
 * Whether a workflow still has work ahead of it.
 *
 * Pending and paused workflows are live even though neither is executing at
 * this instant: pending still has its first step ahead of it, and paused will
 * resume from its checkpoint. This mirrors WorkflowStatus.Live in the Go
 * domain model.
 */
export function isWorkflowLive(
  state: WorkflowState,
  stopReason: WorkflowStopReason,
): boolean {
  return (
    state === WorkflowState.PENDING ||
    state === WorkflowState.ACTIVE ||
    isWorkflowPaused(state, stopReason)
  );
}

/** Whether a workflow stopped for one particular reason. */
export function workflowStoppedBecause(
  state: WorkflowState,
  stopReason: WorkflowStopReason,
  expectedReason: WorkflowStopReason,
): boolean {
  return state === WorkflowState.STOPPED && stopReason === expectedReason;
}
