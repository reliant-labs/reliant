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

/**
 * Adapt a wire `WorkflowExecutionData` to the `WorkflowExecution` shape the
 * screen renders.
 *
 * These are two parallel models of the same thing: the protobuf-derived one
 * (types/chat) carries `status: string`, while the view model
 * (ExecutionSidebar/types) narrows it to a four-value union that
 * ExecutionStatusPill uses as a lookup key. A plain cast would let an
 * unrecognized status through and index the table with a miss, producing an
 * element with `className={undefined}` — a silently unstyled pill.
 *
 * So the status is VALIDATED rather than asserted: anything outside the union
 * falls back to "running", which is the honest reading of "the server told us
 * something we don't have a rendering for, and this execution exists".
 *
 * Converging the two workflow models is the real fix; this keeps the boundary
 * honest until then.
 */
function toScreenExecution(
  execution: WorkflowExecutionData | null | undefined,
): WorkflowExecution | undefined {
  if (!execution) return undefined;

  const known: WorkflowExecution["status"][] = [
    "running",
    "completed",
    "failed",
    "cancelled",
  ];
  const rawStatus = String(execution.status);
  const status = known.includes(rawStatus as WorkflowExecution["status"])
    ? (rawStatus as WorkflowExecution["status"])
    : "running";

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
