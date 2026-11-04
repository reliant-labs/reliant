// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows list's data (WORKFLOW_UI.md §2.2): every workflow in a
 * project — hidden ones included, since this is the management view — plus
 * the ones that failed to parse, and the management actions on them. Across
 * every project it is the same read per project: a project's own workflows
 * come from its repository, so no one project's list covers another's.
 *
 * The list is the generated ListWorkflows entry with includeHidden (the chat
 * pickers read the one without it). Each action is a workflow-queries write,
 * which refetches every mounted workflow read before it resolves, so this
 * list and the composer's picker never disagree.
 */

import { useMemo } from "react";
import { useMutation, useQueries, type UseQueryResult } from "@tanstack/react-query";

import {
  copyWorkflow,
  createWorkflowDraft,
  deleteWorkflow,
  importWorkflow,
  setWorkflowVisibility,
  useWorkflowList,
  workflowListFromProto,
  WORKFLOW_STALE_TIME_MS,
  type ListWorkflowsResult,
} from "./workflow-queries";
import { workflowServiceQueries } from "./workflow-service-hooks_gen";

/** Every workflow in the project and every one that failed to load. */
export function useWorkflowLibrary(projectId: string | undefined) {
  return useWorkflowList(projectId, { includeHidden: true });
}

export interface ProjectLibraries {
  /** Each project's list, keyed by project id, once it has loaded. */
  byProject: Map<string, ListWorkflowsResult>;
  isLoading: boolean;
  /** Every project's list has loaded (or failed): only then is the union complete. */
  complete: boolean;
  /** The projects whose list could not be loaded. */
  failed: string[];
  refetch: () => void;
}

/**
 * Every listed project's library, one read per project from the same cache
 * entries the per-project list uses — so opening one project's list after the
 * all-projects one costs nothing.
 */
export function useProjectLibraries(projectIds: readonly string[], enabled = true): ProjectLibraries {
  const ids = projectIds.join("\n");
  // Stable, so combine re-runs only when a result changes and the Map holds
  // between renders (the list's view memo depends on it).
  const combine = useMemo(() => combineLibraries(ids ? ids.split("\n") : []), [ids]);
  return useQueries({
    queries: projectIds.map((projectId) =>
      workflowServiceQueries.listWorkflows(
        { projectId, includeHidden: true },
        { enabled, staleTime: WORKFLOW_STALE_TIME_MS, select: workflowListFromProto },
      ),
    ),
    combine,
  });
}

function combineLibraries(projectIds: readonly string[]) {
  return (results: Array<UseQueryResult<ListWorkflowsResult>>): ProjectLibraries => {
    const byProject = new Map<string, ListWorkflowsResult>();
    const failed: string[] = [];
    results.forEach((result, i) => {
      const projectId = projectIds[i]!;
      if (result.data) byProject.set(projectId, result.data);
      if (result.isError) failed.push(projectId);
    });
    return {
      byProject,
      isLoading: results.some((r) => r.isLoading),
      complete: results.every((r) => r.isSuccess || r.isError),
      failed,
      refetch: () => results.forEach((r) => void r.refetch()),
    };
  };
}

export function useDeleteWorkflow(projectId: string) {
  return useMutation({
    mutationFn: (filename: string) => deleteWorkflow(projectId, filename),
  });
}

export function useCopyWorkflow(projectId: string) {
  return useMutation({
    mutationFn: (sourceName: string) => copyWorkflow(projectId, sourceName),
  });
}

export function useSetWorkflowVisibility(projectId: string) {
  return useMutation({
    mutationFn: ({ filename, hidden }: { filename: string; hidden: boolean }) =>
      setWorkflowVisibility(projectId, filename, hidden),
  });
}

/** "New workflow": one draft per call, so callers run it only on a user action. */
export function useCreateWorkflowDraft(projectId: string) {
  return useMutation({
    mutationFn: (opts: { title: string; template?: string }) => createWorkflowDraft(projectId, opts),
  });
}

export function useImportWorkflow(projectId: string) {
  return useMutation({
    mutationFn: ({ yaml, overwrite }: { yaml: string; overwrite: boolean }) =>
      importWorkflow(projectId, yaml, overwrite),
  });
}
