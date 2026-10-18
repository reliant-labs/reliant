import { useMemo, useState, memo, useEffect } from "react";
import { ChevronDown, ChevronRight, ListTree, CheckCircle2, AlertCircle, Loader2, XCircle } from "lucide-react";
import { cn } from "../../lib/utils";
import { ToolExecution, durableStatusToDisplayStatus, workflowLifecycleToDisplayStatus, type ToolResultData } from "./ToolExecution";
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

// Mirrors the subset of lib/messageProcessor's ToolCallData this group reads.
// `input` is OPTIONAL there (undefined while the call is still streaming), and
// this file already branches on `lead.call.input === undefined` below — the
// type just disagreed with the code.
type ToolCallData = {
  id: string;
  name: string;
  input?: Record<string, unknown> | string;
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
  chatId?: string;
  showRichContent?: boolean;
  onSelectThread?: (threadId: string | null) => void;
  density?: "compact" | "card" | "minimal";
}

function ToolExecutionGroupComponent({
  executions,
  messageId,
  defaultCollapsed: _defaultCollapsed = true,
  chatId,
  showRichContent = false,
  onSelectThread,
  density = "compact",
}: ToolExecutionGroupProps) {
  const hasPendingApprovals = useMemo(() => {
    return executions.some(execution => execution.approval?.status === ApprovalStatus.PENDING);
  }, [executions]);

  // Honor the per-tool collapse settings: the group starts open only if at
  // least one tool in it would have opened on its own. Collapsing the group
  // while its rows were individually collapsed would hide them twice over.
  const surface = useSurface();
  const [expanded, setExpanded] = useState(() =>
    surface !== "mobile" && executions.some(({ call }) => !shouldToolBeCollapsed(call.name, surface))
  );

  useEffect(() => {
    if (hasPendingApprovals) {
      setExpanded(true);
    }
  }, [hasPendingApprovals]);

  // Pick up preference changes made in Settings without a reload.
  useEffect(() => {
    const applyNewDefault = () => {
      if (surface === "mobile") return;
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
          ? (() => {
              const workflow = findWorkflowById(allWorkflows, call.childWorkflowId);
              return workflowLifecycleToDisplayStatus(
                workflow?.state,
                workflow?.stopReason,
              ) ?? "executing";
            })()
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

  const mobileLabel = useMemo(() => {
    if (executions.length === 1) {
      const call = executions[0].call;
      let input = call.input;
      if (typeof input === "string") { try { input = JSON.parse(input); } catch { input = undefined; } }
      if (/shell|bash/.test(call.name) && input && typeof input === "object") {
        const command = input.command ?? input.cmd;
        if (typeof command === "string") return `${summary.running ? "Running" : "Ran"} ${truncate(command, 72)}`;
      }
      const formatted = formatToolParams(call.name, call.input as ToolInput);
      return formatted?.summary ? truncate(formatted.summary, 72) : call.name.replace(/^mcp__.*__/, "");
    }
    const counts = new Map<string, number>();
    executions.forEach(({ call }) => {
      const label = /shell|bash/.test(call.name) ? "commands" : /read|view_file/.test(call.name) ? "files read" : /search|grep|glob/.test(call.name) ? "searches" : /edit|write|patch/.test(call.name) ? "file edits" : "other actions";
      counts.set(label, (counts.get(label) || 0) + 1);
    });
    return Array.from(counts, ([label, count]) => {
      if (label === "commands") return `${summary.running ? "Running" : "Ran"} ${count} command${count === 1 ? "" : "s"}`;
      if (label === "files read") return `Read ${count} file${count === 1 ? "" : "s"}`;
      if (label === "file edits") return `Edited ${count} file${count === 1 ? "" : "s"}`;
      if (label === "searches") return `${count} search${count === 1 ? "" : "es"}`;
      return `${count} other action${count === 1 ? "" : "s"}`;
    }).join(" · ");
  }, [executions, summary.running]);

  const hasAnyErrors = summary.errors > 0;
  const hasAnyCancelled = summary.cancelled > 0;
  const isAllCompleted = summary.completed === summary.total && summary.total > 0;

  return (
    <div
      className={cn(
        surface === "mobile" ? "overflow-hidden rounded-lg" : "border overflow-hidden",
        density === "card" ? "rounded-xl shadow-sm" : "rounded-lg",
        density === "minimal" && "rounded-md shadow-none",
        surface === "mobile" && !hasAnyErrors ? "bg-transparent" : hasAnyErrors
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
      <button
        type="button"
        aria-expanded={expanded}
        className={cn(
          "w-full text-left flex items-center justify-between cursor-pointer hover:bg-muted/30 transition-colors",
          surface === "mobile" ? "min-h-[44px] px-3 py-2" : density === "card" ? "px-3 py-2" : "px-1.5 py-1"
        )}
        onClick={() => setExpanded((v) => !v)}
      >
        <div className="flex min-w-0 items-center gap-2">
          {surface === "mobile" && summary.running > 0 ? <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-muted-foreground" /> : <ListTree className="w-3.5 h-3.5 shrink-0 text-muted-foreground/60" />}
          <span className={cn("min-w-0 text-muted-foreground", surface === "mobile" ? "truncate text-sm" : "text-xs font-mono font-medium")} >
            {surface === "mobile" ? `${mobileLabel}${hasAnyErrors ? " · Failed" : hasAnyCancelled ? " · Cancelled" : ""}` : headerLabel}
          </span>
        </div>

        <div className="flex items-center gap-1">
          {surface !== "mobile" && summary.running > 0 && (
            <span className="inline-flex items-center gap-1 text-xs font-mono text-primary">
              <Loader2 className="w-3.5 h-3.5 animate-spin" /> {summary.running}
            </span>
          )}
          {surface !== "mobile" && summary.completed > 0 && (
            <span className="inline-flex items-center gap-1 text-xs font-mono text-success-ink/60">
              <CheckCircle2 className="w-3.5 h-3.5" /> {summary.completed}
            </span>
          )}
          {surface !== "mobile" && summary.cancelled > 0 && (
            <span className="inline-flex items-center gap-1 text-xs font-mono text-muted-foreground">
              <XCircle className="w-3.5 h-3.5" /> {summary.cancelled}
            </span>
          )}
          {surface !== "mobile" && summary.errors > 0 && (
            <span className="inline-flex items-center gap-1 text-xs font-mono text-warning-ink">
              <AlertCircle className="w-3.5 h-3.5" /> {summary.errors}
            </span>
          )}

          {expanded ? (
            <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" />
          ) : (
            <ChevronRight className="w-3.5 h-3.5 text-muted-foreground" />
          )}
        </div>
      </button>

      {/* Show all tool calls when expanded */}
      {expanded && (
        <div className={cn("border-t border-muted/50 p-1", density === "card" ? "space-y-2" : "space-y-1")}>
          {executions.map((execution, index) => {
            return (
              <ToolExecution
                key={`${messageId || "msg"}-group-exec-${index}-${execution.call.id}`}
                toolCall={execution.call}
                toolResult={execution.result}
                status={execution.status}
                onCancel={execution.onCancel}
                onConvertToBackground={execution.onConvertToBackground}
                approval={execution.approval}
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
