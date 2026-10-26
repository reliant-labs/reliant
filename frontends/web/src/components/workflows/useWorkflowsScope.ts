// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's ONE project scope, read the same way on every page.
 *
 * The header's project menu sets it (`?allProjects=true`, or a project); the
 * Workflows list, a workflow's Triggers and Recent runs, and Runs all honour
 * it, so switching tabs never silently changes what "everything" means. With
 * no current project there is nothing to narrow to, so it is every project
 * whatever the URL says.
 */

import { useSearch } from "@tanstack/react-router";

import { useProjectStore } from "@/store/projectStore";

export interface WorkflowsScope {
  /** The current project, whatever the scope. */
  currentProjectId?: string;
  /** Every project is in scope. */
  allProjects: boolean;
  /** The project in scope; undefined when it is every project. */
  projectId?: string;
}

export function workflowsScope(allProjectsParam: boolean | undefined, currentProjectId: string | undefined): WorkflowsScope {
  const allProjects = !!allProjectsParam || !currentProjectId;
  return { currentProjectId, allProjects, projectId: allProjects ? undefined : currentProjectId };
}

export function useWorkflowsScope(): WorkflowsScope {
  const { allProjects } = useSearch({ strict: false }) as { allProjects?: boolean };
  const currentProjectId = useProjectStore((state) => state.currentProject?.id);
  return workflowsScope(allProjects, currentProjectId);
}
