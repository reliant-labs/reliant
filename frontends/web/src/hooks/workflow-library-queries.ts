// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows Library's data (WORKFLOW_UI.md §2.2): every workflow in a
 * project — hidden ones included, since this is the management view — plus
 * the ones that failed to parse, and the management actions on them.
 *
 * The list is the generated ListWorkflows entry with includeHidden (the chat
 * pickers read the one without it). Each action is a workflow-queries write,
 * which refetches every mounted workflow read before it resolves, so this
 * list and the composer's picker never disagree.
 */

import { useMutation } from "@tanstack/react-query";

import {
  copyWorkflow,
  createWorkflowDraft,
  deleteWorkflow,
  importWorkflow,
  setWorkflowVisibility,
  useWorkflowList,
} from "./workflow-queries";

/** Every workflow in the project and every one that failed to load. */
export function useWorkflowLibrary(projectId: string | undefined) {
  return useWorkflowList(projectId, { includeHidden: true });
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
