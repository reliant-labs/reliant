// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's paths, in one place (WORKFLOW_UI.md §1.2–1.3).
 *
 * One area, two tabs: Workflows (every definition, and whether it runs by
 * itself — its triggers, health and next run, inline) and Runs (the log of
 * every execution, and of every trigger event that started none). An
 * automation is not a third object: it is a workflow with triggers, so it is
 * configured on the workflow's page and listed on the Workflows tab. The
 * Automations tab that used to repeat that list is gone; its paths, like the
 * older `/workflow` hub, `/runs` and `/automations`, survive only as
 * redirects (workflowsAreaRoutes.tsx). This module and that one are the only
 * files that may spell those retired paths; workflowsAreaLinks.test.ts
 * enforces it.
 *
 * Dependency-free on purpose: projectStore, ModernApp and the onboarding
 * wizard all ask "is this a workflow page?", and none of them may import the
 * route tree to find out.
 */

export const WORKFLOWS_AREA_PATH = "/workflows";
/** The Workflows tab's list; a workflow's page is `${this}/<ref>`. */
export const WORKFLOWS_LIBRARY_PATH = "/workflows/library";
export const WORKFLOWS_RUNS_PATH = "/workflows/runs";
/** Retired: the Automations tab. A redirect only. */
const RETIRED_AUTOMATIONS_PATH = "/workflows/automations";

export type WorkflowsTab = "workflows" | "runs";

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
  { key: "workflows", label: "Workflows", path: WORKFLOWS_LIBRARY_PATH, listLabel: "All workflows" },
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
 *   /workflow                     → /workflows/library   (the hub)
 *   /runs[/<id>]                  → /workflows/runs[/<id>]
 *   /automations, /workflows/automations
 *                                 → /workflows/library?show=on&allProjects=true
 *                                   (what the Automations tab listed)
 *   /automations/<id>, /workflows/automations/<id>
 *                                 → the automation redirect, which looks the
 *                                   trigger up and opens its workflow's page
 *                                   (automationRedirectPath)
 */
export function legacyWorkflowsPath(pathname: string): string | null {
  if (pathname === "/workflow") return WORKFLOWS_LIBRARY_PATH;
  if (isAtOrBelow(pathname, "/runs")) return `/workflows${pathname}`;
  if (pathname === RETIRED_AUTOMATIONS_PATH || pathname === "/automations") return ON_WORKFLOWS_HREF;
  // /workflows/automations/<id> is the resolving redirect itself.
  if (pathname.startsWith("/automations/")) return `/workflows${pathname}`;
  return null;
}

/** The Workflows tab narrowed to what runs by itself, in every project. */
export const ON_WORKFLOWS_HREF = `${WORKFLOWS_LIBRARY_PATH}?show=on&allProjects=true`;

/**
 * The redirect an automation id resolves through: the trigger is looked up
 * (it names its workflow and project) and the page replaces itself with
 * automationHref(trigger). Old links and notifications carry only the id.
 */
export function automationRedirectPath(triggerId: string): string {
  return `${RETIRED_AUTOMATIONS_PATH}/${encodeURIComponent(triggerId)}`;
}

/** The DOM id of one project's group on a workflow page's Triggers card. */
export function automationAnchorId(projectId: string): string {
  return `automation-${projectId}`;
}

/** The DOM id of one trigger's row on a workflow page's Triggers card. */
export function triggerAnchorId(triggerId: string): string {
  return `trigger-${triggerId}`;
}

/**
 * A workflow's page, optionally scrolled to one trigger, or to its triggers in
 * one project. `?project=` makes that project current, so the page reads it.
 */
export function workflowPageHref(
  workflowRef: string,
  projectId?: string,
  options: { automation?: boolean; triggerId?: string } = {},
): string {
  const path = `${WORKFLOWS_LIBRARY_PATH}/${encodeURIComponent(workflowRef)}`;
  const query = projectId ? `?${new URLSearchParams({ project: projectId }).toString()}` : "";
  const hash = options.triggerId
    ? `#${triggerAnchorId(options.triggerId)}`
    : projectId && options.automation
      ? `#${automationAnchorId(projectId)}`
      : "";
  return `${path}${query}${hash}`;
}

/**
 * Where an automation — any trigger, declared in the workflow file or added by
 * hand — is opened: its row on its workflow's page. There is no other page.
 * A caller that knows only the trigger's id gets the redirect that looks the
 * rest up.
 */
export function automationHref(trigger: { id: string; workflow?: string; projectId?: string }): string {
  if (!trigger.workflow) return automationRedirectPath(trigger.id);
  return workflowPageHref(trigger.workflow, trigger.projectId || undefined, { triggerId: trigger.id });
}

/** The builder for a workflow; a built-in opens read-only there. */
export function workflowEditHref(workflow: string): string {
  return `/workflow/${encodeURIComponent(workflow.startsWith("builtin://") ? workflow.slice("builtin://".length) : workflow)}`;
}
