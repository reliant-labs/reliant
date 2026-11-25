// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's ONE project scope, read the same way on every page.
 *
 * Inside the one shell it is the nav's scope (lib/urlState/shellScopeStore:
 * one project, several, or every one), the scope the chat list beside it
 * shows, so the area never lists runs or workflows of projects the nav does
 * not show. The area's header menu changes the nav's scope.
 *
 * With no nav on screen (the area's own full-page layout), the header's
 * project menu sets it (`?allProjects=true`, or a project). With no current
 * project there is nothing to narrow to, so it is every project whatever the
 * URL says.
 *
 * Either way `?allProjects=true` widens it to every project (a workflow's
 * "show every project"), and the Workflows list, a workflow's Triggers and
 * Recent runs, and Runs all honour it, so switching tabs never silently
 * changes what "everything" means.
 */

import { useMemo } from "react";
import { useSearch } from "@tanstack/react-router";

import { useShellScopeStore } from "@/lib/urlState/shellScopeStore";
import type { ShellScope } from "@/lib/urlState/shellScope";
import { useProjectStore } from "@/store/projectStore";

export interface WorkflowsScope {
  /** The current project, whatever the scope: what the area acts in (Run, New). */
  currentProjectId?: string;
  /** Every project is in scope. */
  allProjects: boolean;
  /** The project in scope; undefined when it is several or every project. */
  projectId?: string;
  /** The projects in scope when it is several (the nav's choice of several); undefined otherwise. */
  projectIds?: readonly string[];
}

export function workflowsScope(
  allProjectsParam: boolean | undefined,
  currentProjectId: string | undefined,
  nav: ShellScope | null = null,
): WorkflowsScope {
  if (nav && !allProjectsParam) {
    if (nav.mode === "one") return { currentProjectId: nav.projectId, allProjects: false, projectId: nav.projectId };
    if (nav.mode === "many") {
      // Act in the current project when it is one of them, else in the first.
      const current = currentProjectId && nav.projectIds.includes(currentProjectId) ? currentProjectId : nav.projectIds[0];
      return { currentProjectId: current, allProjects: false, projectIds: nav.projectIds };
    }
    return { currentProjectId, allProjects: true };
  }
  const allProjects = !!allProjectsParam || !currentProjectId;
  return { currentProjectId, allProjects, projectId: allProjects ? undefined : currentProjectId };
}

/** Whether `projectId` is in `scope`. */
export function inWorkflowsScope(scope: WorkflowsScope, projectId: string): boolean {
  if (scope.allProjects) return true;
  if (scope.projectIds) return scope.projectIds.includes(projectId);
  return scope.projectId === projectId;
}

export function useWorkflowsScope(): WorkflowsScope {
  const { allProjects } = useSearch({ strict: false }) as { allProjects?: boolean };
  const currentProjectId = useProjectStore((state) => state.currentProject?.id);
  const nav = useShellScopeStore((state) => state.published?.scope ?? null);
  return useMemo(() => workflowsScope(allProjects, currentProjectId, nav), [allProjects, currentProjectId, nav]);
}
