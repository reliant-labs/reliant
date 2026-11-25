/**
 * Sub-agents a turn started together, under one quiet header:
 * "12 agents · 8 done · 3 running · 1 failed".
 *
 * Each agent is one dense row (ToolExecution's sub-agent row). The group is
 * open while any agent runs and closed once all have finished, so finished
 * delegation recedes; a failed agent stays on screen even while the group is
 * closed, because it is the one the user may need to act on. The user's own
 * toggle wins until the page reloads.
 */

import { memo, useMemo, useState, type ComponentProps } from "react";
import { Loader2, Users, XCircle } from "lucide-react";
import { QuietRow } from "./QuietRow";
import { ToolExecution, durableStatusToDisplayStatus, workflowLifecycleToDisplayStatus } from "./ToolExecution";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { findWorkflowById } from "../../lib/workflowTree";
import type { WorkflowExecutionData } from "../../types/chat";

type ToolExecutionGroupExecution = {
  call: ComponentProps<typeof ToolExecution>["toolCall"];
  result?: ComponentProps<typeof ToolExecution>["toolResult"];
  approval?: ComponentProps<typeof ToolExecution>["approval"];
  status?: ComponentProps<typeof ToolExecution>["status"];
  onCancel?: (id: string) => void;
  onConvertToBackground?: (id: string) => void;
};

export type SubAgentState = "running" | "done" | "failed" | "cancelled";

/** Where one spawn stands: its child workflow decides once it has one. */
export function subAgentState(
  { call, result, status }: Pick<ToolExecutionGroupExecution, "call" | "result" | "status">,
  workflows: WorkflowExecutionData[],
): SubAgentState {
  const child = call.childWorkflowId ? findWorkflowById(workflows, call.childWorkflowId) : undefined;
  const effective = call.childWorkflowId
    ? workflowLifecycleToDisplayStatus(child?.state, child?.stopReason) ?? "executing"
    : status || durableStatusToDisplayStatus(call.durableStatus) || (result ? (result.is_error ? "failed" : "completed") : "executing");
  if (effective === "failed") return "failed";
  if (effective === "completed") return "done";
  if (effective === "cancelled" || effective === "denied") return "cancelled";
  return "running";
}

interface SubAgentGroupProps {
  executions: ToolExecutionGroupExecution[];
  messageId?: string;
  chatId?: string;
  showRichContent?: boolean;
  onSelectThread?: (threadId: string | null) => void;
}

function SubAgentGroupComponent({ executions, messageId, chatId, showRichContent = false, onSelectThread }: SubAgentGroupProps) {
  const { allWorkflows } = useWorkflowExecutions(chatId || null);
  const states = useMemo(
    () => executions.map((execution) => subAgentState(execution, allWorkflows)),
    [executions, allWorkflows],
  );
  const count = (state: SubAgentState) => states.filter((s) => s === state).length;
  const running = count("running");
  const failed = count("failed");
  const done = count("done");
  const cancelled = count("cancelled");

  const [override, setOverride] = useState<boolean | null>(null);
  const expanded = override ?? running > 0;

  const row = (execution: ToolExecutionGroupExecution) => (
    <ToolExecution
      key={`${messageId || "msg"}-agent-${execution.call.id}`}
      toolCall={execution.call}
      toolResult={execution.result}
      status={execution.status}
      onCancel={execution.onCancel}
      onConvertToBackground={execution.onConvertToBackground}
      approval={execution.approval}
      chatId={chatId}
      showRichContent={showRichContent}
      onSelectThread={onSelectThread}
    />
  );

  const parts = [
    `${executions.length} agents`,
    done > 0 && `${done} done`,
    running > 0 && `${running} running`,
    failed > 0 && `${failed} failed`,
    cancelled > 0 && `${cancelled} cancelled`,
  ].filter(Boolean) as string[];
  const label = parts.join(" · ");

  return (
    <div data-testid="sub-agent-group" data-expanded={expanded}>
      <QuietRow
        as="button"
        icon={
          running > 0 ? (
            <Loader2 className="size-3.5 animate-spin text-primary-ink" />
          ) : failed > 0 ? (
            <XCircle className="size-4 text-destructive-ink" />
          ) : (
            <Users className="size-4" />
          )
        }
        expanded={expanded}
        onToggle={() => setOverride(!expanded)}
        ariaLabel={label}
        detail={<div className="flex flex-col">{executions.map(row)}</div>}
      >
        <span className="truncate">{label}</span>
      </QuietRow>
      {/* Closed, the failed agents stay in view: they may need the user. */}
      {!expanded && failed > 0 && (
        <div className="ml-2 border-l border-border-subtle pl-4" data-testid="sub-agent-group-failed">
          {executions.filter((_, i) => states[i] === "failed").map(row)}
        </div>
      )}
    </div>
  );
}

export const SubAgentGroup = memo(SubAgentGroupComponent);
