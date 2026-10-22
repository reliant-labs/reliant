// Copyright (c) 2025 Reliant Labs

/**
 * What the step palette lists, independent of how it is drawn: the core node
 * types as a "Built-in" group, and integration catalog entries from
 * SearchCatalog. One list so there is one place to add anything.
 *
 * Built-ins are matched locally (there are a dozen); integrations are
 * matched by the server, which is what lets the catalog grow to hundreds
 * without the browser ever holding all of it. Before anything is typed the
 * integrations are browsed instead (ListCatalogIntegrations): one row per
 * integration, which expands into its own actions or triggers.
 */

import type { CatalogEntrySummary, CatalogIntegrationListing } from "../../../api/catalog-search-grpc";
import { isAgentBuildingBlock } from "../../../lib/node-metadata";

/** A core node type: added by type, configured by its own panel. */
export interface BuiltinPaletteItem {
  kind: "builtin";
  /** Step type to add ("call_llm", "loop") or "switch" (a canvas-only node). */
  type: string;
  label: string;
  description: string;
  keywords: string;
  /** A low-level agent-loop step, listed under Advanced (see isAgentBuildingBlock). */
  advanced?: boolean;
}

/** The row that shows or hides the Advanced building blocks while browsing. */
export interface AdvancedTogglePaletteItem {
  kind: "advanced-toggle";
  expanded: boolean;
  count: number;
}

/** One integration while browsing; choosing it expands its entries beneath it. */
export interface IntegrationPaletteItem {
  kind: "integration";
  listing: CatalogIntegrationListing;
  expanded: boolean;
}

/** "Show more" at the end of an expanded integration with another page. */
export interface MoreEntriesPaletteItem {
  kind: "more-entries";
  integrationId: string;
  remaining: number;
}

/** A built-in trigger source with no catalog entry: schedule, webhook, workflow event. */
export interface BuiltinTriggerPaletteItem {
  kind: "builtin-trigger";
  source: "schedule" | "webhook" | "workflow_event";
  label: string;
  description: string;
  keywords: string;
}

export interface CatalogPaletteItem {
  kind: "catalog";
  entry: CatalogEntrySummary;
  /** Listed beneath its expanded integration rather than as a search result. */
  nested?: boolean;
}

export type PaletteItem =
  | BuiltinPaletteItem
  | BuiltinTriggerPaletteItem
  | CatalogPaletteItem
  | AdvancedTogglePaletteItem
  | IntegrationPaletteItem
  | MoreEntriesPaletteItem;

export function paletteItemKey(item: PaletteItem): string {
  switch (item.kind) {
    case "builtin":
      return `builtin:${item.type}`;
    case "builtin-trigger":
      return `builtin-trigger:${item.source}`;
    case "catalog":
      return `catalog:${item.entry.ref}`;
    case "advanced-toggle":
      return "advanced-toggle";
    case "integration":
      return `integration:${item.listing.integration.id}`;
    case "more-entries":
      return `more-entries:${item.integrationId}`;
  }
}

export function paletteItemLabel(item: PaletteItem): string {
  switch (item.kind) {
    case "catalog":
      return item.entry.displayName;
    case "integration":
      return item.listing.integration.displayName;
    case "advanced-toggle":
      return "Advanced: agent building blocks";
    case "more-entries":
      return item.remaining > 0 ? `Show ${item.remaining} more` : "Show more";
    default:
      return item.label;
  }
}

/** How a browse row counts what expanding it shows: "3 actions", "1 trigger". */
export function entryCountLabel(count: number, kind: "action" | "trigger"): string {
  return `${count} ${kind}${count === 1 ? "" : "s"}`;
}

/** Control flow is drawn by the canvas, not listed by ListNodes. */
export const CONTROL_FLOW_ITEMS: readonly BuiltinPaletteItem[] = [
  { kind: "builtin", type: "loop", label: "Loop", description: "Repeat a body while a condition holds", keywords: "loop repeat while iterate for each" },
  { kind: "builtin", type: "switch", label: "Switch", description: "Branch on a condition", keywords: "switch branch if condition case" },
  { kind: "builtin", type: "router", label: "Router", description: "Let a model pick which workflow to run", keywords: "router route pick choose llm" },
  { kind: "builtin", type: "join", label: "Join", description: "Wait for parallel branches", keywords: "join merge wait parallel" },
];

export const BUILTIN_TRIGGER_ITEMS: readonly BuiltinTriggerPaletteItem[] = [
  { kind: "builtin-trigger", source: "schedule", label: "Schedule", description: "On a cron schedule or an interval", keywords: "schedule cron time interval daily hourly nightly" },
  { kind: "builtin-trigger", source: "webhook", label: "Webhook", description: "When something POSTs to this workflow's URL", keywords: "webhook http post url zapier hook" },
  { kind: "builtin-trigger", source: "workflow_event", label: "When a workflow finishes", description: "When a run of another workflow finishes, fails or blocks", keywords: "workflow run finished failed blocked event chain" },
];

/**
 * Node types ListNodes returns that the palette must not offer as a plain
 * step. `action` is the generic integration node: it is added by choosing an
 * action from the catalog, which sets its `uses`.
 */
const HIDDEN_NODE_TYPES = new Set(["action"]);

interface NodeLike {
  id: string;
  displayName: string;
  description: string;
}

/**
 * Words a node is found by beyond its label and description. The renamed
 * nodes keep their old names and YAML types as keywords, so "execute" and
 * "invoke" still find them.
 */
const EXTRA_KEYWORDS: Record<string, string> = {
  execute_tools: "execute tools tool calls agent loop",
  invoke_tool: "invoke tool call direct",
  workflow: "agent subworkflow sub-workflow",
};

export function builtinItemsFromNodes(nodes: readonly NodeLike[]): BuiltinPaletteItem[] {
  const fromCatalog = nodes
    .filter((node) => !HIDDEN_NODE_TYPES.has(node.id))
    .filter((node) => !CONTROL_FLOW_ITEMS.some((item) => item.type === node.id))
    .map<BuiltinPaletteItem>((node) => ({
      kind: "builtin",
      type: node.id,
      label: node.displayName || node.id,
      description: node.description,
      keywords: [node.id.replace(/_/g, " "), EXTRA_KEYWORDS[node.id]].filter(Boolean).join(" "),
      advanced: isAgentBuildingBlock(node.id),
    }));
  // Core steps first, then control flow, then the agent building blocks, so
  // the Advanced group sits at the end of the built-ins.
  return [...fromCatalog.filter((item) => !item.advanced), ...CONTROL_FLOW_ITEMS, ...fromCatalog.filter((item) => item.advanced)];
}

/**
 * Local match for built-ins: every query word must prefix a word of the
 * label, description or keywords — the same rule SearchCatalog applies, so
 * both groups narrow alike as the user types.
 */
export function matchesBuiltin(item: BuiltinPaletteItem | BuiltinTriggerPaletteItem, query: string): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const haystack = `${item.label} ${item.description} ${item.keywords}`.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
  return words.every((word) => haystack.some((candidate) => candidate.startsWith(word)));
}
