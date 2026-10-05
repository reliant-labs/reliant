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
 *     whose health is FAILING. A row there is taken OUT of its section, never
 *     listed twice, as on the Automations tab (automationGrouping.ts).
 */

import type { InvalidWorkflow, WorkflowResponse } from "@/api/workflow-grpc";
import type { RunSummary } from "@/api/run-grpc";
import type { Trigger } from "@/api/trigger-grpc";
import { automationHealth } from "@/lib/automationHealth";
import type { LibrarySortKey, LibrarySourceKey } from "@/routeSchemas";
import { getWorkflowDisplayName, normalizeWorkflowRef } from "../../workflow/useWorkflowInputs";

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
  /** Per workflow ref (normalized): how many automations run it. */
  automationCounts: Map<string, number>;
  /** Per workflow ref (normalized): how many of those are FAILING. */
  failingCounts: Map<string, number>;
  /** The search or source left nothing, though the library is not empty. */
  noMatches: boolean;
}

export const NEEDS_ATTENTION_LABEL = "Needs attention";

const SOURCE_LABEL: Record<"yours" | "builtin", string> = {
  yours: "Your workflows",
  builtin: "Built-in",
};

/**
 * The Automations column's text. `null` means none, which the cell renders as
 * a dash under the column's label — the dash means "no automation runs this
 * workflow", and it is only legible because the column is labelled.
 */
export function automationsSummary(count: number, failing = 0): string | null {
  if (count <= 0) return null;
  const base = `${count} ${count === 1 ? "automation" : "automations"}`;
  return failing > 0 ? `${base} · ${failing} failing` : base;
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
  const failingCounts = new Map<string, number>();
  for (const trigger of triggers) {
    const key = normalizeWorkflowRef(trigger.workflow);
    automationCounts.set(key, (automationCounts.get(key) ?? 0) + 1);
    if (automationHealth(trigger).key === "failing") {
      failingCounts.set(key, (failingCounts.get(key) ?? 0) + 1);
    }
  }

  const query = q?.trim().toLowerCase() ?? "";
  const searched = query
    ? workflows.filter((w) => matches(query, getWorkflowDisplayName(w.name, true), w.name, w.description))
    : workflows;
  const searchedInvalid = query
    ? invalid.filter((w) => matches(query, getWorkflowDisplayName(w.name, true), w.name, w.path))
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
  const isFailing = (w: WorkflowResponse) => (failingCounts.get(normalizeWorkflowRef(w.name)) ?? 0) > 0;
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
  const shown = sections.reduce((total, section) => total + section.workflows.length, 0) + visibleInvalid.length;
  const narrowed = Boolean(query) || Boolean(source);
  return {
    sections,
    rows: sections.flatMap((section) =>
      section.workflows.map((workflow) => ({ workflow, attention: section.key === "attention" })),
    ),
    noWorkflowsOfYourOwn: sections.some((section) => section.key === "yours" && section.workflows.length === 0),
    invalid: visibleInvalid,
    automationCounts,
    failingCounts,
    noMatches: narrowed && shown === 0 && workflows.length + invalid.length > 0,
  };
}
