import { useMemo, useState, memo, useEffect, useCallback } from "react";
import { ChevronDown, ChevronRight, ListTree, CheckCircle2, AlertCircle, Loader2, XCircle } from "lucide-react";
import { cn } from "../../lib/utils";
import { ToolExecution, type ToolResultData } from "./ToolExecution";
import type { ToolApprovalRequest } from "../../api/client";
import { useActivityStore, ChatActivity } from "../../store/activityStore";
import { ApprovalStatus } from "../../gen/reliant/v1/approval_pb";

type ToolCallData = {
  id: string;
  name: string;
  input: Record<string, unknown> | string;
  finished?: boolean;
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
  // Check if chat is no longer running - affects tool display state
  const chatActivity = useActivityStore((s) => s.activities.get(chatId || ""));
  const isWorkflowInactive = chatActivity === ChatActivity.IDLE;
  
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

  // Always start expanded - user can collapse if they want
  const [expanded, setExpanded] = useState(true);

  useEffect(() => {
    if (hasPendingApprovals) {
      setExpanded(true);
    }
  }, [hasPendingApprovals]);

  const summary = useMemo(() => {
    let completed = 0;
    let errors = 0;
    let running = 0;
    let cancelled = 0;

    executions.forEach(({ result, status }) => {
      if (status === "failed" || result?.is_error) {
        errors += 1;
      } else if (status === "completed" || result) {
        completed += 1;
      } else if (status === "cancelled") {
        cancelled += 1;
      } else {
        // Pending/preparing/executing states
        // If workflow is inactive, the tool likely completed (the completion
        // event may not have been tracked). Don't infer cancelled from IDLE
        // since the IDLE event can race ahead of tool completion events.
        if (isWorkflowInactive) {
          completed += 1;
        } else {
          running += 1;
        }
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
  }, [executions, isWorkflowInactive]);

  // Preview tools string for potential future use in collapsed header
  // const previewTools = useMemo(() => {
  //   const list = summary.names.map((n) => `${n}()`);
  //   const joined = list.join(", ");
  //   return joined.length > 60 ? `${joined.slice(0, 57)}…` : joined;
  // }, [summary.names]);

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
          ? "border-success/30 bg-success/5"
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
          <ListTree className="w-3.5 h-3.5 text-muted-foreground" />
          <span className="text-xs font-mono font-medium">
            {summary.total} tool call{summary.total === 1 ? "" : "s"}
          </span>
        </div>

        <div className="flex items-center gap-1">
          {summary.running > 0 && (
            <span className="inline-flex items-center gap-1 text-xs font-mono text-primary">
              <Loader2 className="w-3.5 h-3.5 animate-spin" /> {summary.running}
            </span>
          )}
          {summary.completed > 0 && (
            <span className="inline-flex items-center gap-1 text-xs font-mono text-success">
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