// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's paths, in one place (WORKFLOW_UI.md §1.2–1.3).
 *
 * One area, three tabs in the order of the jobs — find it, turn it on, watch
 * it: Library (definitions), Automations (what runs by itself) and Runs (every
 * execution). They replaced three separate places — the
 * `/workflow` hub, `/runs` and `/automations` — which survive only as
 * redirects (workflowsAreaRoutes.tsx). This module and that one are the only
 * files that may spell those retired paths; workflowsAreaLinks.test.ts
 * enforces it.
 *
 * Dependency-free on purpose: projectStore, ModernApp and the onboarding
 * wizard all ask "is this a workflow page?", and none of them may import the
 * route tree to find out.
 */

export const WORKFLOWS_AREA_PATH = "/workflows";
export const WORKFLOWS_LIBRARY_PATH = "/workflows/library";
export const WORKFLOWS_RUNS_PATH = "/workflows/runs";
export const WORKFLOWS_AUTOMATIONS_PATH = "/workflows/automations";

export type WorkflowsTab = "library" | "runs" | "automations";

export interface WorkflowsTabInfo {
  key: WorkflowsTab;
  label: string;
  /** The tab's list path; everything below it is one of its detail pages. */
  path: string;
  /** What a detail page's exit returns to. */
  listLabel: string;
}

/** The tabs, in the order the tab bar renders them. */
export const WORKFLOWS_TABS: readonly WorkflowsTabInfo[] = [
  { key: "library", label: "Library", path: WORKFLOWS_LIBRARY_PATH, listLabel: "Library" },
  { key: "automations", label: "Automations", path: WORKFLOWS_AUTOMATIONS_PATH, listLabel: "All automations" },
  { key: "runs", label: "Runs", path: WORKFLOWS_RUNS_PATH, listLabel: "All runs" },
];

function isAtOrBelow(pathname: string, path: string): boolean {
  return pathname === path || pathname.startsWith(`${path}/`);
}

/** Which tab a pathname belongs to; null outside the area. */
export function workflowsTabForPath(pathname: string): WorkflowsTab | null {
  return WORKFLOWS_TABS.find((tab) => isAtOrBelow(pathname, tab.path))?.key ?? null;
}

/** A tab's detail page (`/workflows/runs/<id>`), as opposed to its list. */
export function isWorkflowsDetailPath(pathname: string): boolean {
  return WORKFLOWS_TABS.some((tab) => pathname.startsWith(`${tab.path}/`));
}

export function isWorkflowsAreaPath(pathname: string): boolean {
  return isAtOrBelow(pathname, WORKFLOWS_AREA_PATH);
}

/** The builder: `/workflow/$workflowName` and `/workflow/new`. Unchanged by the merge. */
export function isWorkflowBuilderPath(pathname: string): boolean {
  return pathname.startsWith("/workflow/");
}

/**
 * Any page of the workflow surface — the area or the builder. These pages
 * render their own chrome outside the project shell, so the project is
 * background context there and a project switch must not navigate away.
 */
export function isWorkflowSurfacePath(pathname: string): boolean {
  return isWorkflowsAreaPath(pathname) || isWorkflowBuilderPath(pathname);
}

/**
 * Where a retired path now lives, or null if it is not one. The search string
 * is the caller's to carry; this maps only the path.
 *
 *   /workflow                 → /workflows/library   (the hub)
 *   /runs[/<id>]              → /workflows/runs[/<id>]
 *   /automations[/<id>]       → /workflows/automations[/<id>]
 */
export function legacyWorkflowsPath(pathname: string): string | null {
  if (pathname === "/workflow") return WORKFLOWS_LIBRARY_PATH;
  if (isAtOrBelow(pathname, "/runs")) return `/workflows${pathname}`;
  if (isAtOrBelow(pathname, "/automations")) return `/workflows${pathname}`;
  return null;
}

/** The DOM id of one project's automation on a workflow page ("Turned on" card). */
export function automationAnchorId(projectId: string): string {
  return `automation-${projectId}`;
}

/**
 * A workflow's page, optionally scrolled to its automation in one project.
 * `?project=` makes that project current, so the page reads it.
 */
export function workflowPageHref(workflowRef: string, projectId?: string, options: { automation?: boolean } = {}): string {
  const path = `${WORKFLOWS_LIBRARY_PATH}/${encodeURIComponent(workflowRef)}`;
  const query = projectId ? `?${new URLSearchParams({ project: projectId }).toString()}` : "";
  const hash = projectId && options.automation ? `#${automationAnchorId(projectId)}` : "";
  return `${path}${query}${hash}`;
}

/**
 * Where an automation is opened, decided in this one place: a workflow
 * automation (an activation of a declared trigger) is configured on its
 * workflow's page, a custom automation on its own page.
 */
export function automationHref(trigger: { id: string; workflow: string; workflowTrigger?: string; projectId: string }): string {
  if (trigger.workflowTrigger && trigger.workflow) {
    return workflowPageHref(trigger.workflow, trigger.projectId, { automation: true });
  }
  return `${WORKFLOWS_AUTOMATIONS_PATH}/${encodeURIComponent(trigger.id)}`;
}

/** The builder for a workflow; a built-in opens read-only there. */
export function workflowEditHref(workflow: string): string {
  return `/workflow/${encodeURIComponent(workflow.startsWith("builtin://") ? workflow.slice("builtin://".length) : workflow)}`;
}
