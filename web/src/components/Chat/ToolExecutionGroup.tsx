import { useMemo, useState, memo, useEffect, useCallback } from "react";
import { ChevronDown, ChevronRight, ListTree, CheckCircle2, AlertCircle, Loader2, XCircle } from "lucide-react";
import { cn } from "../../lib/utils";
import { ToolExecution, durableStatusToDisplayStatus, workflowStatusToDisplayStatus, type ToolResultData } from "./ToolExecution";
import type { ToolApprovalRequest } from "../../api/client";
import { shouldToolBeCollapsed, TOOL_COLLAPSE_SETTINGS_EVENT } from "../Settings/ToolCallSettings";
import { useSurface } from "../../lib/surfaceContext";
import { formatToolParams, type ToolInput, isSpawnTool } from "../../lib/toolFormatters";
import type { ToolCallStatus } from "../../gen/reliant/v1/chat_pb";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { findWorkflowById } from "../../lib/workflowTree";

/**
 * The lead preview shares a single line with the trailing "and N other tools",
 * so it gets a hard cap rather than relying on CSS truncation — otherwise a
 * long command would push the count out of view.
 */
const LEAD_PREVIEW_MAX = 48;

function truncate(text: string, max: number): string {
  const oneLine = text.replace(/\s+/g, " ").trim();
  return oneLine.length > max ? `${oneLine.slice(0, max - 1)}…` : oneLine;
}
import { ApprovalStatus } from "../../gen/reliant/v1/approval_pb";

type ToolCallData = {
  id: string;
  name: string;
  input: Record<string, unknown> | string;
  finished?: boolean;
  durableStatus?: ToolCallStatus;
  /** For a spawn call, the workflow it started (tool_calls.child_workflow_id). */
  childWorkflowId?: string;
};

interface ToolExecutionGroupProps {
  executions: Array<{
    call: ToolCallData;
    result?: ToolResultData;
    approval?: ToolApprovalRequest;
    status?: "pending" | "preparing" | "requested" | "writing_input" | "executing" | "cancelling" | "cancelled" | "completed" | "backgrounded" | "denied" | "failed";
    onCancel?: (id: string) => void;
    onConvertToBackground?: (id: string) => void;
  }>;
  messageId?: string;
  defaultCollapsed?: boolean;
  approvals?: ToolApprovalRequest[];
  chatId?: string;
  showRichContent?: boolean;
  onSelectThread?: (threadId: string | null) => void;
  density?: "compact" | "card" | "minimal";
}

function ToolExecutionGroupComponent({
  executions,
  messageId,
  defaultCollapsed: _defaultCollapsed = true,
  approvals = [],
  chatId,
  showRichContent = false,
  onSelectThread,
  density = "compact",
}: ToolExecutionGroupProps) {
  // Check both embedded approvals and separate approvals list
  const getApprovalStatus = useCallback((toolCallId: string): ToolApprovalRequest | undefined => {
    return approvals.find(approval => approval.tool_call_id === toolCallId);
  }, [approvals]);

  const hasPendingApprovals = useMemo(() => {
    return executions.some(execution => {
      const approval = execution.approval || getApprovalStatus(execution.call.id);
      return approval && approval.status === ApprovalStatus.PENDING;
    });
  }, [executions, getApprovalStatus]);

  // Honor the per-tool collapse settings: the group starts open only if at
  // least one tool in it would have opened on its own. Collapsing the group
  // while its rows were individually collapsed would hide them twice over.
  const surface = useSurface();
  const [expanded, setExpanded] = useState(() =>
    executions.some(({ call }) => !shouldToolBeCollapsed(call.name, surface))
  );

  useEffect(() => {
    if (hasPendingApprovals) {
      setExpanded(true);
    }
  }, [hasPendingApprovals]);

  // Pick up preference changes made in Settings without a reload.
  useEffect(() => {
    const applyNewDefault = () => {
      setExpanded(executions.some(({ call }) => !shouldToolBeCollapsed(call.name, surface)));
    };
    window.addEventListener(TOOL_COLLAPSE_SETTINGS_EVENT, applyNewDefault);
    return () => window.removeEventListener(TOOL_COLLAPSE_SETTINGS_EVENT, applyNewDefault);
  }, [executions, surface]);

  // Async spawn returns its tool result the moment the child agent is
  // dispatched, so a spawn call's own status/result reads "completed" while
  // the agent it started keeps running for minutes. For a dispatched spawn
  // entry, the child workflow is the authority on whether it's still running.
  const { allWorkflows } = useWorkflowExecutions(chatId || null);

  const summary = useMemo(() => {
    let completed = 0;
    let errors = 0;
    let running = 0;
    let cancelled = 0;

    executions.forEach(({ call, result, status }) => {
      const isSpawn = isSpawnTool(call.name);
      const effectiveStatus =
        isSpawn && call.childWorkflowId
          ? workflowStatusToDisplayStatus(findWorkflowById(allWorkflows, call.childWorkflowId)?.status) ?? "executing"
          : status || durableStatusToDisplayStatus(call.durableStatus);

      if (effectiveStatus === "failed" || (!isSpawn && result?.is_error)) {
        errors += 1;
      } else if (effectiveStatus === "completed" || (!isSpawn && result)) {
        completed += 1;
      } else if (effectiveStatus === "cancelled") {
        cancelled += 1;
      } else {
        // Pending/preparing/executing states, with no durable status either.
        running += 1;
      }
    });

    const names = executions.map(({ call }) => call.name);
    return {
      total: executions.length,
      completed,
      errors,
      running,
      cancelled,
      names,
    };
  }, [executions, allWorkflows]);

  // Lead with a concrete call so the collapsed row says what happened rather
  // than how many times it happened, and preview its argument the same way an
  // expanded row would — `bash(Run the tests)` beats a bare `bash`. A spawn in
  // the group always takes the lead: delegating to a sub-agent is the most
  // notable thing in any group that contains one.
  const headerLabel = useMemo(() => {
    const lead =
      executions.find(({ call }) => isSpawnTool(call.name)) ??
      executions[0];
    if (!lead) return "tool";

    const rawName = lead.call.name;
    const name = rawName.startsWith("mcp__") ? rawName.split("__").pop() || rawName : rawName;

    // Same formatter the individual rows use, so the preview text matches what
    // you see after expanding (for bash this is the model's description). A
    // call whose input has not streamed in yet has nothing to preview, so the
    // row shows the bare name until it arrives.
    const formatted =
      lead.call.input === undefined
        ? undefined
        : formatToolParams(rawName, lead.call.input as ToolInput);
    const preview = formatted?.summary?.trim();
    const head = preview ? `${name}(${truncate(preview, LEAD_PREVIEW_MAX)})` : name;

    const rest = executions.length - 1;
    if (rest <= 0) return head;
    return `${head} and ${rest} other ${rest === 1 ? "tool" : "tools"}`;
  }, [executions]);

  const hasAnyErrors = summary.errors > 0;
  const hasAnyCancelled = summary.cancelled > 0;
  const isAllCompleted = summary.completed === summary.total && summary.total > 0;

  return (
    <div
      className={cn(
        "border overflow-hidden",
        density === "card" ? "rounded-xl shadow-sm" : "rounded-lg",
        density === "minimal" && "rounded-md shadow-none",
        hasAnyErrors
          ? "border-warning/40 bg-warning/5"
          : hasAnyCancelled
          ? "border-muted/60 bg-muted/10"
          : isAllCompleted
          // The common case recedes: a green-tinted box per group turned the
          // transcript into a wall of colour.
          ? "border-success/15 bg-success/[0.02]"
          : "border-muted/50 elevation-1"
      )}
    >
      <div
        className={cn(
          "flex items-center justify-between cursor-pointer hover:bg-muted/30 transition-colors",
          density === "card" ? "px-3 py-2" : "px-1.5 py-1"
        )}
        onClick={() => setExpanded((v) => !v)}
      >
        <div className="flex items-center gap-1">
          <ListTree className="w-3.5 h-3.5 text-muted-foreground/60" />
          <span className="text-xs font-mono font-medium text-muted-foreground">
            {headerLabel}
          </span>
        </div>

        <div className="flex items-center gap-1">
          {summary.running > 0 && (
            <span className="inline-flex items-center gap-1 text-xs font-mono text-primary">
              <Loader2 className="w-3.5 h-3.5 animate-spin" /> {summary.running}
            </span>
          )}
          {summary.completed > 0 && (
            <span className="inline-flex items-center gap-1 text-xs font-mono text-success/60">
              <CheckCircle2 className="w-3.5 h-3.5" /> {summary.completed}
            </span>
          )}
          {summary.cancelled > 0 && (
            <span className="inline-flex items-center gap-1 text-xs font-mono text-muted-foreground">
              <XCircle className="w-3.5 h-3.5" /> {summary.cancelled}
            </span>
          )}
          {summary.errors > 0 && (
            <span className="inline-flex items-center gap-1 text-xs font-mono text-warning">
              <AlertCircle className="w-3.5 h-3.5" /> {summary.errors}
            </span>
          )}

          {expanded ? (
            <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" />
          ) : (
            <ChevronRight className="w-3.5 h-3.5 text-muted-foreground" />
          )}
        </div>
      </div>

      {/* Show all tool calls when expanded */}
      {expanded && (
        <div className={cn("border-t border-muted/50 p-1", density === "card" ? "space-y-2" : "space-y-1")}>
          {executions.map((execution, index) => {
            const approval = execution.approval || getApprovalStatus(execution.call.id);

            return (
              <ToolExecution
                key={`${messageId || "msg"}-group-exec-${index}-${execution.call.id}`}
                toolCall={execution.call}
                toolResult={execution.result}
                status={execution.status}
                onCancel={execution.onCancel}
                onConvertToBackground={execution.onConvertToBackground}
                approval={approval}
                chatId={chatId}
                showRichContent={showRichContent}
                onSelectThread={onSelectThread}
                density={density}
              />
            );
          })}
        </div>
      )}
    </div>
  );
}

export const ToolExecutionGroup = memo(ToolExecutionGroupComponent);