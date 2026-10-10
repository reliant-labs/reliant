/**
 * The workflow configuration a chat composer reads on every open: the
 * definition of the selected workflow (its inputs, preset tags) and the
 * presets compatible with it.
 *
 * Both used to be fetched in a bare useEffect, so every mount of the composer
 * — every chat opened, every return to a chat — re-issued GetWorkflow and
 * ListPresetsForWorkflow even though neither had changed, and the params UI
 * waited on them. In React Query they are read from cache on the first frame
 * and refetched in the background only once stale.
 *
 * Freshness: everything in this app that changes a workflow or a preset goes
 * through globalDataStore.refetchWorkflows / refetchPresets, which invalidate
 * these keys (see invalidateWorkflowConfig). The staleTime only bounds how
 * long an edit made OUTSIDE the app (a workflow YAML changed in an editor)
 * can go unnoticed — and even then the next window focus after it lapses
 * picks the change up.
 */

import { useQuery } from "@tanstack/react-query";
import { workflowGrpc } from "../api/workflow-grpc";
import { presetGrpc, type Preset } from "../api/preset-grpc";
import { queryClient } from "../lib/query-client";

export const WORKFLOW_CONFIG_STALE_TIME_MS = 5 * 60_000;

export const workflowConfigKeys = {
  all: ["workflow-config"] as const,
  definitions: () => [...workflowConfigKeys.all, "definition"] as const,
  definition: (projectId: string, workflowName: string) =>
    [...workflowConfigKeys.definitions(), projectId, workflowName] as const,
  presetLists: () => [...workflowConfigKeys.all, "presets-for-workflow"] as const,
  presetsForWorkflow: (projectId: string, workflowName: string) =>
    [...workflowConfigKeys.presetLists(), projectId, workflowName] as const,
  defaultPresetMaps: () => [...workflowConfigKeys.all, "default-presets"] as const,
  defaultPresets: (projectId: string, workflowName: string) =>
    [...workflowConfigKeys.defaultPresetMaps(), projectId, workflowName] as const,
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

/** Presets compatible with a workflow, in an explicit project. */
export function usePresetsForWorkflowQuery(
  projectId: string | undefined,
  workflowName: string,
) {
  return useQuery<Preset[]>({
    queryKey: workflowConfigKeys.presetsForWorkflow(projectId ?? "", workflowName),
    queryFn: () => presetGrpc.listPresetsForWorkflow(projectId!, workflowName),
    enabled: !!projectId && !!workflowName,
    staleTime: WORKFLOW_CONFIG_STALE_TIME_MS,
  });
}

/**
 * A workflow's default preset per group (group name -> preset name), for an
 * imperative caller: served from cache while fresh, fetched otherwise.
 */
export function fetchDefaultPresets(
  projectId: string,
  workflowName: string,
): Promise<Record<string, string>> {
  return queryClient.fetchQuery({
    queryKey: workflowConfigKeys.defaultPresets(projectId, workflowName),
    queryFn: () => presetGrpc.getDefaultPresets(projectId, workflowName),
    staleTime: WORKFLOW_CONFIG_STALE_TIME_MS,
  });
}

/**
 * Something changed the project's presets or workflows. Presets are matched
 * against the workflow's inputs server-side, so a workflow change can change
 * the compatible preset list as well as the definition.
 */
export function invalidateWorkflowConfig(change: "presets" | "workflows"): void {
  void queryClient.invalidateQueries({ queryKey: workflowConfigKeys.presetLists() });
  void queryClient.invalidateQueries({ queryKey: workflowConfigKeys.defaultPresetMaps() });
  if (change === "workflows") {
    void queryClient.invalidateQueries({ queryKey: workflowConfigKeys.definitions() });
  }
}
