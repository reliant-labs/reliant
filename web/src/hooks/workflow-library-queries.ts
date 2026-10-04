// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows Library's data (WORKFLOW_UI.md §2.2): every workflow in a
 * project — hidden ones included, since this is the management view — plus
 * the ones that failed to parse, and the management actions on them.
 *
 * React Query rather than the global workflow store: that store holds the
 * chat-safe list (runnable, visible), which is the wrong list to manage, and
 * it follows the current project implicitly.
 */

import { useEffect } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { workflowGrpc } from "../api/workflow-grpc";
import { useGlobalDataStore } from "../store/globalDataStore";

export const workflowLibraryKeys = {
  all: ["workflow-library"] as const,
  list: (projectId: string) => [...workflowLibraryKeys.all, "list", projectId] as const,
};

/**
 * Every workflow in the project and every one that failed to load. Refetched
 * whenever anything saves a workflow (the builder dispatches `workflow-saved`).
 */
export function useWorkflowLibrary(projectId: string | undefined) {
  const queryClient = useQueryClient();
  useEffect(() => {
    const onSaved = () => void queryClient.invalidateQueries({ queryKey: workflowLibraryKeys.all });
    window.addEventListener("workflow-saved", onSaved);
    return () => window.removeEventListener("workflow-saved", onSaved);
  }, [queryClient]);

  return useQuery({
    queryKey: workflowLibraryKeys.list(projectId ?? ""),
    queryFn: () => workflowGrpc.listWorkflowsWithErrors(projectId!, true),
    enabled: !!projectId,
  });
}

/**
 * After any change to the set of workflows: refresh this list AND the global
 * store the composer's workflow picker reads, so the two never disagree.
 */
function useRefreshWorkflows(projectId: string) {
  const queryClient = useQueryClient();
  return async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: workflowLibraryKeys.list(projectId) }),
      useGlobalDataStore
        .getState()
        .refetchWorkflows(projectId)
        .catch(() => undefined),
    ]);
  };
}

export function useDeleteWorkflow(projectId: string) {
  const refresh = useRefreshWorkflows(projectId);
  return useMutation({
    mutationFn: (filename: string) => workflowGrpc.deleteWorkflow(projectId, filename),
    onSuccess: refresh,
  });
}

export function useCopyWorkflow(projectId: string) {
  const refresh = useRefreshWorkflows(projectId);
  return useMutation({
    mutationFn: (sourceName: string) => workflowGrpc.copyWorkflow(projectId, sourceName),
    onSuccess: refresh,
  });
}

export function useSetWorkflowVisibility(projectId: string) {
  const refresh = useRefreshWorkflows(projectId);
  return useMutation({
    mutationFn: ({ filename, hidden }: { filename: string; hidden: boolean }) =>
      workflowGrpc.setWorkflowVisibility(projectId, filename, hidden),
    onSuccess: refresh,
  });
}

export function useImportWorkflow(projectId: string) {
  const refresh = useRefreshWorkflows(projectId);
  return useMutation({
    mutationFn: ({ yaml, overwrite }: { yaml: string; overwrite: boolean }) =>
      workflowGrpc.importWorkflow(projectId, yaml, overwrite),
    onSuccess: async (response) => {
      if (response.success) await refresh();
    },
  });
}
