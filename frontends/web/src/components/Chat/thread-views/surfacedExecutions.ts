/**
 * Which tool calls the simplified thread keeps on screen, each as a row of
 * its own at its place in the transcript, instead of inside a fold: the ones
 * the user has to see or act on.
 *
 * - a question to the user: ask_user (answered or not — the card carries the
 *   answer) and request_machine;
 * - a call waiting on the user's approval;
 * - a generation tool's image or video, the result the user asked for;
 * - a drafted automation, once its draft is back (its "Review & create" card);
 * - a deploy that put something live (its app card).
 *
 * The last three depend on a result or an approval, so a call can move out
 * of its fold when one arrives (and back into it once an approval settles).
 */

import { ApprovalStatus, type Approval } from "../../../gen/reliant/v1/approval_pb";
import type { ToolExecution, ToolResultsByCallId } from "../../../lib/messageProcessor";
import { isRequestMachineTool } from "../../../lib/toolFormatters";
import { deployOfToolCall } from "../../Apps/DeployedAppCard";
import { isDraftAutomationTool } from "../tool-renderers/DraftAutomationToolRenderer";
import { isSurfacedMediaTool, type SurfacedExecutionPredicate } from "./simplifiedTurns";

/** What the decision reads of a call: a structural subset of ToolExecution. */
export interface SurfacedCandidate {
  call: Pick<ToolExecution["call"], "name" | "input">;
  result?: { content: string; is_error?: boolean };
  approval?: Pick<Approval, "status">;
}

export function isSurfacedExecution({ call, result, approval }: SurfacedCandidate): boolean {
  if (call.name === "ask_user" || isRequestMachineTool(call.name)) return true;
  if (approval?.status === ApprovalStatus.PENDING) return true;
  if (isSurfacedMediaTool(call.name)) return true;
  if (isDraftAutomationTool(call.name)) return Boolean(result && !result.is_error);
  return deployOfToolCall(call.name, call.input, result) !== null;
}

/**
 * isSurfacedExecution over calls read without results or approvals (the
 * timeline's segment reader): joins the chat's result index and approvals in
 * the same way ChatMessage does — the embedded approval first, then the list
 * by content block id.
 */
export function createSurfacedPredicate(
  results: ToolResultsByCallId,
  approvals: readonly Approval[],
): SurfacedExecutionPredicate {
  const approvalsByBlock = new Map<string, Approval>();
  for (const approval of approvals) {
    if (approval.entityId) approvalsByBlock.set(approval.entityId, approval);
  }
  return (execution) =>
    isSurfacedExecution({
      call: execution.call,
      result: execution.result ?? results[execution.call.id],
      approval:
        execution.approval ??
        (execution.call.content_block_id ? approvalsByBlock.get(execution.call.content_block_id) : undefined),
    });
}
