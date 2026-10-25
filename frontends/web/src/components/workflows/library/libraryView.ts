// Copyright (c) 2025 Reliant Labs

/**
 * What the Library shows for a given search, source and sort
 * (WORKFLOW_UI.md §2.2). Pure, so the rows a user sees for a URL are pinned by
 * tests rather than by a rendered page.
 *
 *   - Search matches the display name, the stored ref and the description,
 *     case-insensitively. A definition that failed to load matches on its
 *     name and file path: it has no description to search.
 *   - Source narrows to Your workflows (yours and the project's), Built-in,
 *     or Failed to load.
 *   - Sort by name keeps the origin sections. "Recently run" is ONE list,
 *     newest last run first, then the never-run ones by name: sections would
 *     split the most relevant rows across headings (§2.2). LastRunPerWorkflow
 *     is already fetched for the rows' Last run column, so it costs nothing.
 *   - "Needs attention" is pinned on top: every workflow with an automation
 *     in this project that needs the user (lib/automationHealth.ts — Broken,
 *     Needs setup, Can't start, Failing, …). A row there is taken OUT of its
 *     section, never listed twice, as on the Automations tab.
 */

import type { InvalidWorkflow, WorkflowResponse } from "@/api/workflow-grpc";
import type { RunSummary } from "@/hooks/run-queries";
import type { Trigger } from "@/api/trigger-grpc";
import { automationHealth, worstHealth, type AutomationHealthDisplay } from "@/lib/automationHealth";
import type { LibrarySortKey, LibrarySourceKey } from "@/routeSchemas";
import { normalizeWorkflowRef } from "../../workflow/useWorkflowInputs";
import { workflowDisplayName } from "../../../lib/workflowDisplayName";

export interface LibraryViewInput {
  workflows: WorkflowResponse[];
  invalid: InvalidWorkflow[];
  triggers: Trigger[];
  /** Newest run per workflow, keyed by the workflow's stored ref. */
  lastRuns?: Map<string, RunSummary>;
  q?: string;
  source?: LibrarySourceKey;
  sort?: LibrarySortKey;
}

export type LibrarySectionKey = "attention" | "yours" | "builtin" | "all";

export interface LibrarySection {
  key: LibrarySectionKey;
  label: string;
  workflows: WorkflowResponse[];
}

/** One row of the Library table. */
export interface LibraryRow {
  workflow: WorkflowResponse;
  /** Pinned on top: an automation that runs it is FAILING. */
  attention: boolean;
}

export interface LibraryView {
  /** In display order. "attention" is present only when non-empty. */
  sections: LibrarySection[];
  /**
   * The table's rows: the sections, flattened in display order. One table
   * rather than a card per section, so every column lines up under one header
   * row; a row's section survives as its Source column and `attention` flag.
   */
  rows: LibraryRow[];
  /**
   * "Your workflows" is empty and nothing narrowed it: the page offers
   * create-your-own, which the empty section used to carry.
   */
  noWorkflowsOfYourOwn: boolean;
  /** Definitions that failed to load, after search and source. */
  invalid: InvalidWorkflow[];
  /**
   * Hidden built-ins a person can run directly, not offered in the composer.
   * Shown collapsed. Automation-only ones are an automation's internal steps
   * and are not listed.
   */
  buildingBlocks: WorkflowResponse[];
  /** Per workflow ref (normalized): how many automations run it. */
  automationCounts: Map<string, number>;
  /** Per workflow ref (normalized): the worst health of the automations that run it. */
  worstHealth: Map<string, AutomationHealthDisplay>;
  /** The search or source left nothing, though the library is not empty. */
  noMatches: boolean;
}

export const NEEDS_ATTENTION_LABEL = "Needs attention";

const SOURCE_LABEL: Record<"yours" | "builtin", string> = {
  yours: "Your workflows",
  builtin: "Built-in",
};

/**
 * The "Turned on" column's text: the worst state of what runs the workflow in
 * this project, in the health vocabulary's words, or Off for a workflow that
 * could run by itself and does not. `null` means it declares nothing to turn
 * on and nothing runs it: the cell is a dash under the column's label.
 */
export function turnedOnSummary(
  health: AutomationHealthDisplay | undefined,
  declaresTriggers: boolean,
): { text: string; attention: boolean } | null {
  if (!health) return declaresTriggers ? { text: "Off", attention: false } : null;
  if (health.needsAttention || health.key === "paused") return { text: health.label, attention: health.needsAttention };
  return { text: "On", attention: false };
}

function matches(query: string, ...fields: (string | undefined)[]): boolean {
  return fields.some((field) => field?.toLowerCase().includes(query));
}

export function libraryView({
  workflows,
  invalid,
  triggers,
  lastRuns,
  q,
  source,
  sort = "name",
}: LibraryViewInput): LibraryView {
  const automationCounts = new Map<string, number>();
  const worst = new Map<string, AutomationHealthDisplay>();
  for (const trigger of triggers) {
    const key = normalizeWorkflowRef(trigger.workflow);
    automationCounts.set(key, (automationCounts.get(key) ?? 0) + 1);
    const health = automationHealth(trigger);
    const before = worst.get(key);
    worst.set(key, before ? worstHealth([before, health])! : health);
  }

  const query = q?.trim().toLowerCase() ?? "";
  const visible = workflows.filter((w) => !(w.source === "builtin" && w.isHidden));
  // A hidden built-in that also refuses a chat start (automation_only) is a
  // step inside an automation pack (Autopilot's lease and label helpers), not
  // something a person runs: it is listed nowhere, only ref'd.
  const buildingBlocks = workflows
    .filter((w) => w.source === "builtin" && w.isHidden && !w.automationOnly)
    .filter((w) => !query || matches(query, workflowDisplayName(w), w.name, w.description));
  const searched = query
    ? visible.filter((w) => matches(query, workflowDisplayName(w), w.name, w.description))
    : visible;
  const searchedInvalid = query
    ? invalid.filter((w) => matches(query, workflowDisplayName(w), w.name, w.path))
    : invalid;

  const byName = (a: WorkflowResponse, b: WorkflowResponse) =>
    normalizeWorkflowRef(a.name).localeCompare(normalizeWorkflowRef(b.name));
  const byRecentRun = (a: WorkflowResponse, b: WorkflowResponse) =>
    (lastRuns?.get(b.name)?.createdAt ?? 0) - (lastRuns?.get(a.name)?.createdAt ?? 0) || byName(a, b);

  const showYours = !source || source === "yours";
  const showBuiltin = !source || source === "builtin";
  const inSource = searched
    .filter((w) => (w.source === "builtin" ? showBuiltin : showYours))
    .sort(sort === "recent" ? byRecentRun : byName);
  const isFailing = (w: WorkflowResponse) => !!worst.get(normalizeWorkflowRef(w.name))?.needsAttention;
  const attention = inSource.filter(isFailing);
  const rest = inSource.filter((w) => !isFailing(w));

  const sections: LibrarySection[] = [];
  if (attention.length > 0) sections.push({ key: "attention", label: NEEDS_ATTENTION_LABEL, workflows: attention });
  if (sort === "recent") {
    if (rest.length > 0) {
      const label = source === "yours" || source === "builtin" ? SOURCE_LABEL[source] : "All workflows";
      sections.push({ key: "all", label, workflows: rest });
    }
  } else {
    // "Your workflows" stays even when empty — it carries the create-your-own
    // prompt — unless a search or the source filter is what emptied it.
    const yours = rest.filter((w) => w.source !== "builtin");
    const builtin = rest.filter((w) => w.source === "builtin");
    if (showYours && (yours.length > 0 || (!query && !source))) {
      sections.push({ key: "yours", label: SOURCE_LABEL.yours, workflows: yours });
    }
    if (showBuiltin && builtin.length > 0) {
      sections.push({ key: "builtin", label: SOURCE_LABEL.builtin, workflows: builtin });
    }
  }

  const visibleInvalid = !source || source === "failed" ? searchedInvalid : [];
  const shown = sections.reduce((total, section) => total + section.workflows.length, 0) + visibleInvalid.length + buildingBlocks.length;
  const narrowed = Boolean(query) || Boolean(source);
  return {
    sections,
    rows: sections.flatMap((section) =>
      section.workflows.map((workflow) => ({ workflow, attention: section.key === "attention" })),
    ),
    noWorkflowsOfYourOwn: sections.some((section) => section.key === "yours" && section.workflows.length === 0),
    invalid: visibleInvalid,
    buildingBlocks,
    automationCounts,
    worstHealth: worst,
    noMatches: narrowed && shown === 0 && workflows.length + invalid.length > 0,
  };
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
 * The automations deleting a user workflow takes with it: every one of the
 * caller's, in any project, that runs it by name. The server deletes the same
 * set in the same operation (bar an automation in a project that syncs its
 * own workflow of that name, which keeps running that one).
 */
export function automationsDeletedWith(workflow: Pick<WorkflowResponse, "name">, triggers: readonly Trigger[]): Trigger[] {
  const slug = workflowSlug(workflow.name);
  if (!slug) return [];
  return triggers.filter(
    (trigger) =>
      !trigger.workflow.startsWith("builtin://") &&
      workflowSlug(trigger.workflow) === slug,
  );
}

/** What the Delete confirmation says happens to the automations that run the workflow. */
export function deletedAutomationsSentence(automations: readonly Trigger[]): string {
  if (automations.length === 0) return "";
  const n = automations.length;
  const names = automations.slice(0, 3).map((a) => `“${a.name}”`).join(", ");
  const more = n > 3 ? `, and ${n - 3} more` : "";
  return `Also deletes ${n === 1 ? "its 1 automation" : `its ${n} automations`}: ${names}${more}.`;
}
