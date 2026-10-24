/**
 * The workflow configuration a chat composer reads on every open: the
 * definition of the selected workflow (its inputs, preset tags). The presets
 * compatible with it are hooks/preset-queries.ts.
 *
 * It used to be fetched in a bare useEffect, so every mount of the composer
 * — every chat opened, every return to a chat — re-issued GetWorkflow even
 * though it had not changed, and the params UI waited on it. In React Query
 * it is read from cache on the first frame and refetched in the background
 * only once stale.
 *
 * Freshness: everything in this app that changes a workflow goes through
 * globalDataStore.refetchWorkflows, which invalidates these keys (see
 * invalidateWorkflowConfig). The staleTime only bounds how long an edit made
 * OUTSIDE the app (a workflow YAML changed in an editor) can go unnoticed —
 * and even then the next window focus after it lapses picks the change up.
 */

import { useQuery } from "@tanstack/react-query";
import { workflowGrpc } from "../api/workflow-grpc";
import { presetServiceKeys } from "./preset-service-hooks_gen";
import { queryClient } from "../lib/query-client";

export const WORKFLOW_CONFIG_STALE_TIME_MS = 5 * 60_000;

export const workflowConfigKeys = {
  all: ["workflow-config"] as const,
  definitions: () => [...workflowConfigKeys.all, "definition"] as const,
  definition: (projectId: string, workflowName: string) =>
    [...workflowConfigKeys.definitions(), projectId, workflowName] as const,
};

/**
 * A workflow's definition by name. `data.workflow` is undefined when the
 * workflow failed to parse (`data.parseError` says why).
 */
export function useWorkflowDefinitionQuery(
  projectId: string | undefined,
  workflowName: string | undefined,
) {
  return useQuery({
    queryKey: workflowConfigKeys.definition(projectId ?? "", workflowName ?? ""),
    queryFn: () => workflowGrpc.getWorkflow(projectId!, { name: workflowName! }),
    enabled: !!projectId && !!workflowName,
    staleTime: WORKFLOW_CONFIG_STALE_TIME_MS,
  });
}

/**
 * Something changed the project's workflows. Presets are matched against the
 * workflow's inputs server-side, so a workflow change can change the
 * compatible preset lists as well as the definition.
 */
export function invalidateWorkflowConfig(): void {
  void queryClient.invalidateQueries({ queryKey: workflowConfigKeys.definitions() });
  void queryClient.invalidateQueries({ queryKey: presetServiceKeys.all });
}
