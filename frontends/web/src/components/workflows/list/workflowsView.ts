// Copyright (c) 2025 Reliant Labs

/**
 * What the Workflows list shows for a given scope, search, filter and sort
 * (WORKFLOW_UI.md §2.2). Pure, so the rows a user sees for a URL are pinned by
 * tests rather than by a rendered page.
 *
 * ONE row per workflow, and on it whether the workflow runs by itself: every
 * trigger in scope that runs it — declared in the workflow file and turned on
 * here, or added by hand — folded into its state (On / Off / partly), its
 * worst health, its soonest next run. An automation is not a separate row or
 * page: it is this workflow's triggers.
 *
 *   - Scope: one project, or every project. In every project a project's own
 *     workflow (from its repository) is a row per project, named with it; a
 *     built-in or yours is one row whose triggers span projects.
 *   - Search matches the display name, the stored ref and the description,
 *     case-insensitively. A definition that failed to load matches on its
 *     name and file path: it has no description to search.
 *   - Show narrows to On (it runs by itself), Needs you, Yours (yours and the
 *     project's), Built-in, or Failed to load.
 *   - Sort by name keeps the origin sections. "Recently run" is ONE list,
 *     newest last run first, then the never-run ones by name.
 *   - "Needs you" is pinned on top: a workflow with a trigger in scope that
 *     needs the user. The predicate is lib/automationUnits' unitsNeedingYou,
 *     the one "needs you" computation every surface uses (the workflow page's
 *     callout too). A row there is taken OUT of its section, never listed
 *     twice.
 *   - A trigger whose workflow is listed nowhere (its file was removed from
 *     the repository) still gets a row, so it can be turned off or removed.
 */

import type { InvalidWorkflow, WorkflowResponse } from "@/hooks/workflow-queries";
import type { RunSummary } from "@/hooks/run-queries";
import type { Trigger } from "@/hooks/trigger-model";
import { worstHealth, type AutomationHealthDisplay } from "@/lib/automationHealth";
import { automationUnits, unitsNeedingYou, type AutomationUnit } from "@/lib/automationUnits";
import { triggerRunsWorkflow } from "@/lib/triggerRail";
import type { LibrarySortKey, WorkflowsShowKey } from "@/routeSchemas";
import { normalizeWorkflowRef } from "../../workflow/useWorkflowInputs";
import { workflowDisplayName } from "../../../lib/workflowDisplayName";

/** A definition, and the project it belongs to when it is a project's own. */
export interface ListedWorkflow {
  workflow: WorkflowResponse;
  /** Set for a project workflow listed across projects: it exists only there. */
  projectId?: string;
  projectName?: string;
}

export interface WorkflowsViewInput {
  workflows: ListedWorkflow[];
  invalid: InvalidWorkflow[];
  /** Every trigger in scope. */
  triggers: Trigger[];
  /** Newest run per workflow, keyed by the workflow's stored ref. */
  lastRuns?: Map<string, RunSummary>;
  /** Whether every definition in scope has loaded: only then is a trigger with no row an orphan. */
  complete?: boolean;
  q?: string;
  show?: WorkflowsShowKey;
  sort?: LibrarySortKey;
}

/**
 * Whether the workflow runs by itself, in scope. "none": nothing runs it and
 * it declares nothing to turn on — the row offers Add trigger… instead.
 */
export type WorkflowRunsByItself = "on" | "partly" | "off" | "none";

/** One row of the Workflows table. */
export interface WorkflowListRow {
  /** Stable: the ref, plus the project for a project's own workflow. */
  key: string;
  workflow: WorkflowResponse;
  projectId?: string;
  projectName?: string;
  /** Every trigger in scope that runs it, declared or added by hand. */
  triggers: Trigger[];
  /** The same triggers, as the automations a person means (one per workflow × project, plus each hand-made one). */
  units: AutomationUnit[];
  /** The worst health of its triggers; absent when none run it. */
  health?: AutomationHealthDisplay;
  state: WorkflowRunsByItself;
  /** It declares `triggers:` of its own. */
  declaresTriggers: boolean;
  /** Pinned on top: a trigger in scope needs the user. */
  needsYou: boolean;
  /** The soonest next run of an On trigger. */
  nextFireAt?: string;
  /** How many projects it is on in. */
  projectsOn: number;
  /** Triggers run it, but no definition by that name is listed. */
  missing: boolean;
}

export type WorkflowsSectionKey = "needs-you" | "yours" | "builtin" | "all";

export interface WorkflowsSection {
  key: WorkflowsSectionKey;
  label: string;
  rows: WorkflowListRow[];
}

export interface WorkflowsView {
  /** In display order. "needs-you" is present only when non-empty. */
  sections: WorkflowsSection[];
  /** The sections, flattened in display order: one table, every column under one header. */
  rows: WorkflowListRow[];
  /** "Your workflows" is empty and nothing narrowed it: the page offers create-your-own. */
  noWorkflowsOfYourOwn: boolean;
  /** Definitions that failed to load, after search and filter. */
  invalid: InvalidWorkflow[];
  /** Hidden built-ins a person can run directly, not offered in the composer. Shown collapsed. */
  buildingBlocks: WorkflowListRow[];
  /** The search or filter left nothing, though the list is not empty. */
  noMatches: boolean;
}

export const NEEDS_YOU_LABEL = "Needs you";

const SOURCE_LABEL: Record<"yours" | "builtin", string> = {
  yours: "Your workflows",
  builtin: "Built-in",
};

function matches(query: string, ...fields: (string | undefined)[]): boolean {
  return fields.some((field) => field?.toLowerCase().includes(query));
}

const refKey = (ref: string) => normalizeWorkflowRef(ref).trim().toLowerCase();

/** A placeholder definition for triggers whose workflow is listed nowhere. */
function missingWorkflow(ref: string): WorkflowResponse {
  return {
    name: ref,
    filename: "",
    stepCount: 0,
    source: ref.startsWith("builtin://") ? "builtin" : "project",
    status: "complete",
    validationErrors: [],
    nodes: [],
    edges: [],
  };
}

/** Folds a workflow's triggers into the row's state. */
export function runsByItself(triggers: readonly Trigger[], declaresTriggers: boolean): WorkflowRunsByItself {
  if (triggers.length === 0) return declaresTriggers ? "off" : "none";
  const on = triggers.filter((t) => t.enabled).length;
  if (on === triggers.length) return "on";
  return on === 0 ? "off" : "partly";
}

function soonest(triggers: readonly Trigger[]): string | undefined {
  let best: string | undefined;
  for (const t of triggers) {
    if (!t.enabled || !t.nextFireAt) continue;
    if (!best || Date.parse(t.nextFireAt) < Date.parse(best)) best = t.nextFireAt;
  }
  return best;
}

function toRow(listed: ListedWorkflow, triggers: Trigger[], missing = false): WorkflowListRow {
  const units = automationUnits(triggers);
  const health = units.length > 0 ? worstHealth(units.map((u) => u.health)) : undefined;
  const declaresTriggers = (listed.workflow.triggers?.length ?? 0) > 0;
  return {
    key: `${refKey(listed.workflow.name)}::${listed.projectId ?? ""}`,
    workflow: listed.workflow,
    projectId: listed.projectId,
    projectName: listed.projectName,
    triggers,
    units,
    health,
    state: runsByItself(triggers, declaresTriggers),
    declaresTriggers,
    needsYou: unitsNeedingYou(units).length > 0,
    nextFireAt: soonest(triggers),
    projectsOn: new Set(triggers.filter((t) => t.enabled).map((t) => t.projectId)).size,
    missing,
  };
}

export function workflowsView({
  workflows,
  invalid,
  triggers,
  lastRuns,
  complete = true,
  q,
  show,
  sort = "name",
}: WorkflowsViewInput): WorkflowsView {
  // Each trigger belongs to the first row that runs it: a project's own
  // workflow in that project, else the built-in or user workflow by ref.
  const claimed = new Set<string>();
  const triggersOf = (listed: ListedWorkflow): Trigger[] => {
    const mine = triggers.filter(
      (t) => !claimed.has(t.id) && triggerRunsWorkflow(t, listed.workflow.name) && (!listed.projectId || t.projectId === listed.projectId),
    );
    for (const t of mine) claimed.add(t.id);
    return mine;
  };
  // Project-scoped definitions claim first, so a project's own workflow wins
  // its triggers over a same-named workflow listed for every project.
  const ordered = [...workflows].sort((a, b) => Number(!a.projectId) - Number(!b.projectId));
  const allRows = new Map(ordered.map((listed) => [listed, toRow(listed, triggersOf(listed))]));

  const orphans: WorkflowListRow[] = [];
  if (complete) {
    const byRef = new Map<string, Trigger[]>();
    for (const t of triggers) {
      if (claimed.has(t.id)) continue;
      const key = `${refKey(t.workflow)}::${t.projectId}`;
      byRef.set(key, [...(byRef.get(key) ?? []), t]);
    }
    for (const rows of byRef.values()) {
      orphans.push(toRow({ workflow: missingWorkflow(rows[0].workflow), projectId: rows[0].projectId, projectName: rows[0].projectName }, rows, true));
    }
  }

  const query = q?.trim().toLowerCase() ?? "";
  const searchHit = (row: WorkflowListRow) =>
    !query || matches(query, workflowDisplayName(row.workflow), row.workflow.name, row.workflow.description);
  const rowOf = (listed: ListedWorkflow) => allRows.get(listed)!;

  const visible = workflows.filter((l) => !(l.workflow.source === "builtin" && l.workflow.isHidden)).map(rowOf);
  // A hidden built-in that also refuses a chat start (automation_only) is a
  // step inside an automation pack, not something a person runs: it is listed
  // nowhere, only ref'd — unless something runs it by itself.
  const buildingBlocks = workflows
    .filter((l) => l.workflow.source === "builtin" && l.workflow.isHidden && (!l.workflow.automationOnly || rowOf(l).triggers.length > 0))
    .map(rowOf)
    .filter(searchHit)
    .filter((row) => showMatches(row, show));
  const searchedInvalid = query ? invalid.filter((w) => matches(query, workflowDisplayName(w), w.name, w.path)) : invalid;

  const byName = (a: WorkflowListRow, b: WorkflowListRow) =>
    refKey(a.workflow.name).localeCompare(refKey(b.workflow.name)) || (a.projectName ?? "").localeCompare(b.projectName ?? "");
  const byRecentRun = (a: WorkflowListRow, b: WorkflowListRow) =>
    (lastRuns?.get(b.workflow.name)?.createdAt ?? 0) - (lastRuns?.get(a.workflow.name)?.createdAt ?? 0) || byName(a, b);

  const inShow = [...visible, ...orphans]
    .filter(searchHit)
    .filter((row) => showMatches(row, show))
    .sort(sort === "recent" ? byRecentRun : byName);
  const needing = inShow.filter((row) => row.needsYou);
  const rest = inShow.filter((row) => !row.needsYou);

  const sections: WorkflowsSection[] = [];
  if (needing.length > 0) sections.push({ key: "needs-you", label: NEEDS_YOU_LABEL, rows: needing });
  const showYours = !show || show === "yours" || show === "on" || show === "needs-you";
  const showBuiltin = !show || show === "builtin" || show === "on" || show === "needs-you";
  if (sort === "recent" || show === "on" || show === "needs-you") {
    if (rest.length > 0) {
      const label = show === "yours" || show === "builtin" ? SOURCE_LABEL[show] : show === "on" ? "On" : "All workflows";
      sections.push({ key: "all", label, rows: rest });
    }
  } else {
    // "Your workflows" stays even when empty — it carries the create-your-own
    // prompt — unless a search or the filter is what emptied it.
    const yours = rest.filter((row) => row.workflow.source !== "builtin");
    const builtin = rest.filter((row) => row.workflow.source === "builtin");
    if (showYours && (yours.length > 0 || (!query && !show))) sections.push({ key: "yours", label: SOURCE_LABEL.yours, rows: yours });
    if (showBuiltin && builtin.length > 0) sections.push({ key: "builtin", label: SOURCE_LABEL.builtin, rows: builtin });
  }

  const visibleInvalid = !show || show === "failed" ? searchedInvalid : [];
  const shown = sections.reduce((total, section) => total + section.rows.length, 0) + visibleInvalid.length + buildingBlocks.length;
  const narrowed = Boolean(query) || Boolean(show);
  return {
    sections,
    rows: sections.flatMap((section) => section.rows),
    noWorkflowsOfYourOwn: sections.some((section) => section.key === "yours" && section.rows.length === 0),
    invalid: visibleInvalid,
    buildingBlocks,
    noMatches: narrowed && shown === 0 && workflows.length + invalid.length + orphans.length > 0,
  };
}

function showMatches(row: WorkflowListRow, show: WorkflowsShowKey | undefined): boolean {
  switch (show) {
    case undefined:
      return true;
    case "on":
      return row.state === "on" || row.state === "partly";
    case "needs-you":
      return row.needsYou;
    case "yours":
      return row.workflow.source !== "builtin";
    case "builtin":
      return row.workflow.source === "builtin";
    case "failed":
      return false;
  }
}

/** The slug a user workflow is addressed by: "project://My Flow" and "my_flow" are both "my-flow". */
function workflowSlug(ref: string): string {
  return ref
    .trim()
    .replace(/^project:\/\//, "")
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "");
}

/**
 * The triggers deleting a user workflow takes with it: every one of the
 * caller's, in any project, that runs it by name. The server deletes the same
 * set in the same operation (bar a trigger in a project that syncs its own
 * workflow of that name, which keeps running that one).
 */
export function automationsDeletedWith(workflow: Pick<WorkflowResponse, "name">, triggers: readonly Trigger[]): Trigger[] {
  const slug = workflowSlug(workflow.name);
  if (!slug) return [];
  return triggers.filter((trigger) => !trigger.workflow.startsWith("builtin://") && workflowSlug(trigger.workflow) === slug);
}

/** What the Delete confirmation says happens to the triggers that run the workflow. */
export function deletedAutomationsSentence(automations: readonly Trigger[]): string {
  if (automations.length === 0) return "";
  const n = automations.length;
  const names = automations
    .slice(0, 3)
    .map((a) => `“${a.name}”`)
    .join(", ");
  const more = n > 3 ? `, and ${n - 3} more` : "";
  return `Also deletes ${n === 1 ? "its 1 trigger" : `its ${n} triggers`}: ${names}${more}.`;
}
