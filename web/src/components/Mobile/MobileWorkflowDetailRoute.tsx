/**
 * Route wrappers for the read-only workflow viewer.
 *
 * `MobileWorkflowScreen` takes a resolved `Workflow` (and optionally a live
 * execution), which is the right shape for the component but not something a
 * route can provide directly. These two wrappers do the lookup:
 *
 *   - `/m/workflows/$workflowName` — from the catalog, no execution context.
 *   - `/m/chats/$chatId/workflow`  — from a running chat, with its execution,
 *     which is the higher-value entry point ("what is my agent doing now").
 *
 * Both existed as links before they existed as routes, so every workflow card
 * and the chat header pill rendered a bare "Not Found".
 */

import { useParams } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import { MobileWorkflowScreen } from "./MobileWorkflowScreen";
import { useWorkflows } from "../../store/globalDataStore";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { normalizeWorkflowRef } from "../workflow/useWorkflowInputs";
import type { WorkflowExecutionData } from "../../types/chat";
import type { WorkflowExecution } from "../Chat/ExecutionSidebar/types";
import {
  WorkflowState,
  WorkflowStopReason,
} from "../../gen/reliant/v1/chat_pb";

/**
 * Adapt a wire `WorkflowExecutionData` to the `WorkflowExecution` shape the
 * screen renders.
 *
 * The wire model preserves workflow state and stop reason separately. The
 * workflow viewer has a smaller display-only status vocabulary, so collapse
 * the pair only at this rendering boundary.
 */
function toScreenExecution(
  execution: WorkflowExecutionData | null | undefined,
): WorkflowExecution | undefined {
  if (!execution) return undefined;

  let status: WorkflowExecution["status"] = "running";
  if (execution.state === WorkflowState.STOPPED) {
    switch (execution.stopReason) {
      case WorkflowStopReason.COMPLETED:
        status = "completed";
        break;
      case WorkflowStopReason.FAILED:
        status = "failed";
        break;
      case WorkflowStopReason.CANCELLED:
        status = "cancelled";
        break;
    }
  }

  // Timestamps cross the boundary as ISO strings on the wire and as epoch
  // millis in the view model, so they are CONVERTED, not spread through.
  const toMillis = (value: string | undefined): number =>
    value ? Date.parse(value) : 0;

  // Only the fields the screen reads are mapped. `children` and `steps` are
  // deliberately empty: this screen renders a status pill, not the execution
  // tree, and recursing two mutually-incompatible shapes to populate
  // something nothing displays would be work in service of a type rather
  // than a user.
  return {
    id: execution.id,
    workflowName: execution.workflowName,
    thread: execution.thread,
    status,
    createdAt: toMillis(execution.createdAt),
    completedAt: execution.completedAt ? toMillis(execution.completedAt) : undefined,
    messageCount: 0,
    children: [],
    steps: [],
  };
}

function Centered({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex flex-1 items-center justify-center px-8 text-center text-sm text-muted-foreground">
      {children}
    </div>
  );
}

/** `/m/workflows/$workflowName` — catalog drill-in, no execution. */
export function MobileWorkflowDetailRoute() {
  // `strict: false` — mobile routes nest under `_authenticated` → `_mobile`,
  // so the registered id is not the path.
  const { workflowName } = useParams({ strict: false });
  const { workflows, loading } = useWorkflows();

  if (loading) {
    return (
      <Centered>
        <Loader2 className="h-5 w-5 animate-spin" />
      </Centered>
    );
  }

  // Refs arrive both bare (`agent`) and qualified (`builtin://agent`), and the
  // URL carries whichever the catalog had — normalize both sides before
  // comparing or the lookup silently misses.
  const target = normalizeWorkflowRef(workflowName ?? "")
    .toLowerCase()
    .trim();
  const workflow = workflows.find(
    (w) => normalizeWorkflowRef(w.name).toLowerCase().trim() === target,
  );

  if (!workflow) return <Centered>Workflow not found</Centered>;

  return <MobileWorkflowScreen workflow={workflow} backTo="/m/workflows" />;
}

/** `/m/chats/$chatId/workflow` — the running workflow behind a chat. */
export function MobileChatWorkflowRoute() {
  const { chatId } = useParams({ strict: false });
  const { workflows, loading } = useWorkflows();
  // `data` is the latest execution for this chat — the one the header pill is
  // reporting on, which is what a user tapping through expects to see.
  const { data: execution } = useWorkflowExecutions(chatId ?? null);

  if (loading) {
    return (
      <Centered>
        <Loader2 className="h-5 w-5 animate-spin" />
      </Centered>
    );
  }

  const ref = execution?.workflowName ?? "";
  const target = normalizeWorkflowRef(ref).toLowerCase().trim();
  const workflow = workflows.find(
    (w) => normalizeWorkflowRef(w.name).toLowerCase().trim() === target,
  );

  if (!workflow) return <Centered>No workflow running for this chat</Centered>;

  return (
    <MobileWorkflowScreen
      workflow={workflow}
      execution={toScreenExecution(execution)}
      chatId={chatId}
      backTo={`/m/chats/${chatId}`}
    />
  );
}
