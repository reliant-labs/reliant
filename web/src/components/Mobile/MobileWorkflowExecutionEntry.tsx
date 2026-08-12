/**
 * Chat entry point into the read-only workflow viewer — "what is this chat
 * running, and where is it up to."
 *
 * Standalone by design: `MobileChatScreen.tsx` renders the same
 * `ChatContainer` desktop uses, so this does not read from that container's
 * internal state. It calls `useWorkflowExecutions(chatId)` directly (the
 * same hook `ChatContainer` uses to feed the desktop-only execution
 * sidebar) and renders nothing when there is no workflow execution yet.
 *
 * Intended placement: `MobileChatScreen`'s header, next to the chat title —
 * drop `<MobileWorkflowExecutionEntry chatId={chatId} />` there. Not wired
 * in here because another agent owns that file.
 */

import { Link } from "@tanstack/react-router";
import { Loader2, Workflow as WorkflowIcon } from "lucide-react";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { ChatWorkflowStatus } from "../../gen/reliant/v1/chat_pb";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";

interface MobileWorkflowExecutionEntryProps {
  chatId: string | null | undefined;
}

export function MobileWorkflowExecutionEntry({
  chatId,
}: MobileWorkflowExecutionEntryProps) {
  const { data: execution } = useWorkflowExecutions(chatId ?? null);

  if (!execution) return null;

  const running =
    execution.status === ChatWorkflowStatus.RUNNING ||
    execution.status === ChatWorkflowStatus.PENDING;

  return (
    <Link
      to="/m/chats/$chatId/workflow"
      params={{ chatId: chatId ?? "" }}
      // 44px floor even though this sits in a 48px header row — it's a
      // small pill, not a full-width row, so the target itself needs the
      // explicit minimum rather than inheriting the row's height.
      className="flex min-h-[44px] items-center gap-1.5 rounded-full bg-muted px-2.5 text-xs text-foreground active:bg-muted/70"
      aria-label={`Workflow: ${getWorkflowDisplayName(execution.workflowName, true)}`}
    >
      {running ? (
        <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-primary" />
      ) : (
        <WorkflowIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      )}
      <span className="max-w-24 truncate">
        {getWorkflowDisplayName(execution.workflowName, true)}
      </span>
    </Link>
  );
}
