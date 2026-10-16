/** Compact workflow navigation, optionally combined with the mobile chat title. */

import { Link } from "@tanstack/react-router";
import { ChevronDown, Workflow as WorkflowIcon } from "lucide-react";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";

interface MobileWorkflowExecutionEntryProps {
  chatId: string | null | undefined;
  title?: string;
}

export function MobileWorkflowExecutionEntry({
  chatId,
  title,
}: MobileWorkflowExecutionEntryProps) {
  const { data: execution } = useWorkflowExecutions(chatId ?? null);

  if (!execution) return title ? <h1 className="truncate text-base font-semibold tracking-tight text-foreground">{title}</h1> : null;

  return (
    <Link
      to="/m/chats/$chatId/workflow"
      params={{ chatId: chatId ?? "" }}
      className="flex min-h-[44px] min-w-0 max-w-full items-center rounded-lg text-left active:bg-foreground/5"
      aria-label={`Workflow: ${getWorkflowDisplayName(execution.workflowName, true)}`}
    >
      <div className="min-w-0">
        {title && <h1 className="truncate text-base font-semibold tracking-tight text-foreground">{title}</h1>}
        <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
          <WorkflowIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="min-w-0 truncate">{getWorkflowDisplayName(execution.workflowName, true)}</span>
          <ChevronDown className="h-3 w-3 shrink-0 opacity-60" />
        </span>
      </div>
    </Link>
  );
}
