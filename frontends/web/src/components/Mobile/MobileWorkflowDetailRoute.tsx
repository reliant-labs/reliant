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

import { useEffect, useMemo } from "react";
import { useParams } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import { MobileWorkflowScreen } from "./MobileWorkflowScreen";
import { useWorkflows } from "../../store/globalDataStore";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { normalizeWorkflowRef } from "../workflow/useWorkflowInputs";
import { useFullStepExecution } from "../workflow/hooks/useFullStepExecution";
import { transformWorkflowExecution } from "../Chat/ExecutionSidebar/transformApiData";
import { runStatus } from "../../lib/runStatus";

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

/**
 * `/m/chats/$chatId/workflow` — the running workflow behind a chat.
 *
 * Node status comes from exactly what the desktop viewer reads (see
 * useExtendedExecutionStatus): the node_execution stream, which is the only
 * source that knows a step inside a loop is running NOW, plus the FULL
 * execution tree — step rows and child workflows — for everything the stream
 * does not carry. This route used to pass an execution with its steps and
 * children stripped and never subscribed to the chat's stream, so a running
 * loop read "Pending" under a "Running" header.
 */
export function MobileChatWorkflowRoute() {
  const { chatId } = useParams({ strict: false });
  const { workflows, loading } = useWorkflows();
  // `data` is the latest execution for this chat — the one the header pill is
  // reporting on, which is what a user tapping through expects to see.
  const { data: execution } = useWorkflowExecutions(chatId ?? null);
  const basicExecution = useMemo(
    () => (execution ? transformWorkflowExecution(execution) : undefined),
    [execution],
  );
  const screenExecution = useFullStepExecution(chatId, basicExecution);

  // The node_execution stream reaches the store only for the subscribed chat.
  // On desktop the viewer sits inside ChatContainer, which asserts that
  // subscription; this route renders no ChatContainer, so it asserts it the
  // same way. Connecting the stream is the shell's job (MobileShell): a
  // subscription asserted before it connects rides the first request.
  const connectionStatus = useGlobalUpdatesStore((s) => s.connectionStatus);
  const reconcileChatSubscription = useGlobalUpdatesStore((s) => s.reconcileChatSubscription);
  useEffect(() => {
    reconcileChatSubscription(chatId ?? null);
  }, [chatId, connectionStatus, reconcileChatSubscription]);

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
      execution={screenExecution}
      runStatus={
        execution
          ? runStatus({
              state: execution.state,
              stopReason: execution.stopReason,
              outcome: execution.outcome,
            })
          : undefined
      }
      chatId={chatId}
      backTo={`/m/chats/${chatId}`}
    />
  );
}
