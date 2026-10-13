// Copyright (c) 2025 Reliant Labs

/**
 * forge's dashboard spec (obsdash, `forge.dashboards/v1`; forge
 * internal/obsdash/spec.go) on the console side: validating a panel someone
 * edited in place, folding the ad-hoc state of a dashboard into a document,
 * and serializing it the way forge does.
 *
 * ── DASHBOARDS ARE CODE ──────────────────────────────────────────────────────
 *
 * There is no server-side dashboard store (owner decision 2). Editing in place
 * changes the URL and nothing else; "Save as code" turns what is on screen
 * into a file for the user to commit at
 * `deploy/observability/dashboards/<name>.json`, which ships with their next
 * release (contract K4). The file is a whole `Spec` — version, project, one
 * dashboard — rather than a bare dashboard, so a consumer can check the
 * version and refuse a shape it does not understand, exactly as forge's
 * `SpecVersion` comment asks.
 *
 * ── SERIALIZED LIKE encoding/json ────────────────────────────────────────────
 *
 * Keys are written in the Go struct's field order and `omitempty` fields are
 * omitted when empty, so the file a user saves diffs cleanly against
 * forge's own `dashboard-spec_gen.json` (pinned by a test against forge's
 * testdata).
 */

import type { AttributeFilter, Dashboard, DashboardFilter, Panel, Predicate, SeriesIntent } from "@/services/controlPlane/observability/types";

import { attributeClass, ENV_KEY, SERVICE_KEY } from "./attributes";

export const SPEC_VERSION = "forge.dashboards/v1";

/** Where a saved dashboard lives in the user's repo. */
export const DASHBOARDS_DIR = "deploy/observability/dashboards";

export const PANEL_KINDS = ["line", "number", "table", "search"] as const;
export const SIGNALS = ["traces", "metrics", "logs"] as const;
export const AGGS = ["count", "count_distinct", "sum", "avg", "max", "quantile", "last"] as const;
export const OPS = ["eq", "neq", "in", "not_in", "prefix", "match", "exists", "gte"] as const;
export const UNITS = ["", "count", "ms", "bytes", "percent"] as const;

export const AGG_LABELS: Record<(typeof AGGS)[number], string> = {
  count: "count",
  count_distinct: "count distinct",
  sum: "sum",
  avg: "average",
  max: "max",
  quantile: "quantile",
  last: "last value",
};

export const OP_LABELS: Record<(typeof OPS)[number], string> = {
  eq: "is",
  neq: "is not",
  in: "is one of",
  not_in: "is none of",
  prefix: "starts with",
  match: "matches (RE2)",
  exists: "exists",
  gte: "at least",
};

/** The fields a panel can name, per signal — what the editor suggests. Open-ended `*.attr.<k>` are accepted too. */
export const FIELD_SUGGESTIONS: Record<(typeof SIGNALS)[number], readonly string[]> = {
  traces: [SERVICE_KEY, ENV_KEY, "span.name", "span.kind", "span.status", "span.status_message", "span.duration", "span.attr.http.route", "span.attr.rpc.method", "resource.attr.service.version"],
  logs: [SERVICE_KEY, ENV_KEY, "log.severity", "log.body", "log.attr.k8s.event.reason", "resource.attr.service.version"],
  metrics: [SERVICE_KEY, ENV_KEY, "resource.attr.service.version", "resource.attr.k8s.container.name"],
};

// ── The document ─────────────────────────────────────────────────────────────

export interface ObsdashSpec {
  version: string;
  project: string;
  dashboards: Dashboard[];
  savedSearches: unknown[];
  alerts: unknown[];
}

/**
 * In-place edits, keyed `<dashboard id>/<panel id>`. A panel REPLACES the
 * dashboard's; null hides it; a key whose panel id the dashboard does not
 * have is a panel the reader added (appended, in key order).
 */
export type PanelEdits = Record<string, Panel | null>;

export function editKey(dashboardId: string, panelId: string): string {
  return `${dashboardId}/${panelId}`;
}

/** The edits that belong to one dashboard, by panel id. */
export function editsFor(edits: PanelEdits | undefined, dashboardId: string): Map<string, Panel | null> {
  const prefix = `${dashboardId}/`;
  const out = new Map<string, Panel | null>();
  for (const [key, panel] of Object.entries(edits ?? {})) {
    if (key.startsWith(prefix)) out.set(key.slice(prefix.length), panel);
  }
  return out;
}

/** The dashboard as the reader sees it: edits applied, hidden panels gone, added ones at the end. */
export function applyEdits(dashboard: Dashboard, edits: PanelEdits | undefined): Dashboard {
  const mine = editsFor(edits, dashboard.id);
  if (mine.size === 0) return dashboard;
  const known = new Set(dashboard.panels.map((p) => p.id));
  const panels: Panel[] = [];
  for (const panel of dashboard.panels) {
    if (!mine.has(panel.id)) panels.push(panel);
    else {
      const edited = mine.get(panel.id);
      if (edited) panels.push({ ...edited, id: panel.id });
    }
  }
  for (const [id, panel] of mine) if (!known.has(id) && panel) panels.push({ ...panel, id });
  return { ...dashboard, panels };
}

/** A panel id not yet used on the dashboard, for "Add panel". */
export function newPanelId(dashboard: Dashboard): string {
  const used = new Set(dashboard.panels.map((p) => p.id));
  for (let i = 1; ; i++) if (!used.has(`custom-${i}`)) return `custom-${i}`;
}

// ── Filters and group-by → panel predicates ──────────────────────────────────

const SIGNAL_ATTR: Record<string, string> = { traces: "span.attr.", logs: "log.attr.", metrics: "metric.attr." };

/**
 * An allowlisted scope key as an obsdash field, on a panel of `signal`.
 * `service.name` and `deployment.environment.name` are named directly; other
 * resource keys are `resource.attr.<k>`; `status` on traces is `span.status`;
 * every other key reads the signal's own attributes (what the server does for
 * a non-resource key: the signal's attribute map first).
 */
export function fieldForKey(key: string, signal: string): string {
  if (key === SERVICE_KEY || key === ENV_KEY) return key;
  if (key.includes(".attr.") || key.startsWith("span.") || key.startsWith("log.")) return key;
  if (key === "status" && signal === "traces") return "span.status";
  if (attributeClass(key) === "resource") return `resource.attr.${key}`;
  return `${SIGNAL_ATTR[signal] ?? "resource.attr."}${key}`;
}

/** RE2 for a case-insensitive substring: the scope's `contains`, which obsdash spells as `match`. */
export function containsPattern(value: string): string {
  return `(?i)${value.replace(/[\\^$.|?*+()[\]{}]/g, "\\$&")}`;
}

export function predicateForFilter(filter: AttributeFilter, signal: string): Predicate {
  const field = fieldForKey(filter.key, signal);
  switch (filter.op) {
    case "exists":
      return { field, op: "exists" };
    case "in":
      return { field, op: "in", values: [...(filter.values ?? [])] };
    case "contains":
      return { field, op: "match", value: containsPattern(filter.value ?? "") };
    default:
      return { field, op: filter.op, value: filter.value ?? "" };
  }
}

export interface SaveAsCodeInput {
  project: string;
  /** The dashboard as on screen — edits already applied. */
  dashboard: Dashboard;
  /** File name without `.json`; also the dashboard's id. */
  name: string;
  title: string;
  /** The scope's attribute filters, baked into every panel's `where`. */
  filters: AttributeFilter[];
  /** The in-place group-by, which replaced each aggregating panel's own. */
  groupBy: string[];
}

/**
 * The dashboard as a document to commit. Environment, service and time range
 * are where the reader is LOOKING and are not baked in — a committed
 * dashboard ships to every environment its release is promoted to, and keeps
 * its own environment and service dropdowns.
 */
export function buildDashboardSpec(input: SaveAsCodeInput): ObsdashSpec {
  const panels = input.dashboard.panels.map((panel) => {
    const where = [...(panel.where ?? []), ...input.filters.map((f) => predicateForFilter(f, panel.signal))];
    const aggregating = panel.kind !== "search";
    return {
      ...panel,
      where,
      groupBy: aggregating && input.groupBy.length ? input.groupBy.map((key) => fieldForKey(key, panel.signal)) : panel.groupBy,
    };
  });
  return {
    version: SPEC_VERSION,
    project: input.project,
    dashboards: [{ ...input.dashboard, id: input.name, title: input.title, panels }],
    savedSearches: [],
    alerts: [],
  };
}

/** A file name a repo can hold: lower case, dashes, no `_gen` suffix (that marks forge's own files). */
export function dashboardFileName(raw: string): string {
  const slug = raw
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .replace(/(-gen)+$/, "");
  return slug.slice(0, 80) || "dashboard";
}

/** The name a generated dashboard's file has, minus `_gen` — saving under it makes the user's copy win (decision 5). */
export function defaultDashboardName(project: string, dashboardId: string): string {
  return dashboardFileName(project ? `${project}-${dashboardId}` : dashboardId);
}

// ── Validation ───────────────────────────────────────────────────────────────

const has = <T extends string>(list: readonly T[], value: unknown): value is T => typeof value === "string" && (list as readonly string[]).includes(value);

function checkPredicates(where: Predicate[] | undefined, at: string, problems: string[]) {
  (where ?? []).forEach((p, i) => {
    const here = `${at} filter ${i + 1}`;
    if (!p.field?.trim()) problems.push(`${here}: choose a field.`);
    if (!has(OPS, p.op)) problems.push(`${here}: "${p.op}" is not an operator.`);
    else if ((p.op === "in" || p.op === "not_in") && !(p.values ?? []).length) problems.push(`${here}: give at least one value.`);
    else if (p.op !== "exists" && p.op !== "in" && p.op !== "not_in" && !(p.value ?? "").trim()) problems.push(`${here}: give a value.`);
  });
}

/**
 * What is wrong with a panel, in words for the editor. Empty means the SHAPE
 * is right; whether the server can compile it (a field it does not know) is
 * its own answer, shown on the panel.
 */
export function panelProblems(panel: Panel): string[] {
  const problems: string[] = [];
  if (!panel.id?.trim()) problems.push("The panel needs an id.");
  if (!panel.title?.trim()) problems.push("Give the panel a title.");
  if (!has(PANEL_KINDS, panel.kind)) problems.push(`"${panel.kind}" is not a panel kind.`);
  if (!has(SIGNALS, panel.signal)) problems.push(`"${panel.signal}" is not a signal.`);
  if (panel.unit !== undefined && !has(UNITS, panel.unit)) problems.push(`"${panel.unit}" is not a unit.`);
  const series = panel.series ?? [];
  if (panel.kind !== "search" && series.length === 0) problems.push("Add at least one series.");
  series.forEach((s: SeriesIntent, i) => {
    const at = `Series ${i + 1}`;
    if (!s.label?.trim()) problems.push(`${at}: give it a label.`);
    if (!has(AGGS, s.agg)) problems.push(`${at}: "${s.agg}" is not an aggregation.`);
    if (s.agg !== "count" && !s.field?.trim()) problems.push(`${at}: ${s.agg} needs a field.`);
    if (s.agg === "quantile" && !(typeof s.quantile === "number" && s.quantile > 0 && s.quantile < 1)) problems.push(`${at}: the quantile is a fraction, like 0.95.`);
    checkPredicates(s.where, at, problems);
  });
  if (panel.ratio && series.length !== 2) problems.push("A ratio divides the first series by the second: it needs exactly two.");
  if ((panel.groupBy ?? []).length > 3) problems.push("Group by at most three fields.");
  if (panel.width !== undefined && panel.width !== 0 && (panel.width < 1 || panel.width > 24)) problems.push("Width is 1 to 24 grid columns.");
  checkPredicates(panel.where, "Panel", problems);
  return problems;
}

export function specProblems(spec: ObsdashSpec): string[] {
  const problems: string[] = [];
  if (spec.version !== SPEC_VERSION) problems.push(`The version must be ${SPEC_VERSION}.`);
  for (const dashboard of spec.dashboards ?? []) {
    if (!dashboard.id?.trim()) problems.push("The dashboard needs an id.");
    if (!dashboard.title?.trim()) problems.push("The dashboard needs a title.");
    const ids = new Set<string>();
    for (const panel of dashboard.panels ?? []) {
      if (ids.has(panel.id)) problems.push(`Two panels share the id "${panel.id}".`);
      ids.add(panel.id);
      for (const problem of panelProblems(panel)) problems.push(`${panel.title || panel.id}: ${problem}`);
    }
  }
  return problems;
}

// ── Serialization (encoding/json order, omitempty) ───────────────────────────

type Json = string | number | boolean | null | Json[] | { [key: string]: Json };

/** Builds an object in the given key order, dropping `omitempty` keys whose value is empty. */
function ordered(entries: [key: string, value: unknown, omitEmpty: boolean][]): { [key: string]: Json } {
  const out: { [key: string]: Json } = {};
  for (const [key, value, omitEmpty] of entries) {
    const empty = value === undefined || value === null || value === "" || value === 0 || value === false || (Array.isArray(value) && value.length === 0);
    if (omitEmpty && empty) continue;
    // A missing required string is Go's zero value, "".
    out[key] = (value ?? "") as Json;
  }
  return out;
}

function predicateJson(p: Predicate) {
  return ordered([
    ["field", p.field, false],
    ["op", p.op, false],
    ["value", p.value, true],
    ["values", p.values, true],
  ]);
}

function seriesJson(s: SeriesIntent) {
  return ordered([
    ["label", s.label, false],
    ["agg", s.agg, false],
    ["quantile", s.quantile, true],
    ["field", s.field, true],
    ["metricType", s.metricType, true],
    ["where", (s.where ?? []).map(predicateJson), true],
  ]);
}

function panelJson(p: Panel) {
  return ordered([
    ["id", p.id, false],
    ["title", p.title, false],
    ["description", p.description, true],
    ["kind", p.kind, false],
    ["signal", p.signal, false],
    // Optional on read: forge omits an empty series list (search panels).
    ["series", (p.series ?? []).map(seriesJson), true],
    ["ratio", p.ratio, true],
    ["where", (p.where ?? []).map(predicateJson), true],
    ["groupBy", p.groupBy, true],
    ["columns", p.columns, true],
    ["unit", p.unit, true],
    ["width", p.width, true],
    ["height", p.height, true],
  ]);
}

function filterJson(f: DashboardFilter) {
  return ordered([
    ["id", f.id, false],
    ["label", f.label, false],
    ["field", f.field, false],
    ["signal", f.signal, true],
  ]);
}

function dashboardJson(d: Dashboard) {
  return ordered([
    ["id", d.id, false],
    ["title", d.title, false],
    ["description", d.description, true],
    ["filters", (d.filters ?? []).map(filterJson), true],
    ["panels", (d.panels ?? []).map(panelJson), false],
  ]);
}

/**
 * The document as forge writes it (json.MarshalIndent, two spaces, then a
 * newline): Go field order, and `<` `>` `&` escaped as encoding/json does.
 */
export function specJson(spec: ObsdashSpec): string {
  const doc = ordered([
    ["version", spec.version, false],
    ["project", spec.project, false],
    ["dashboards", (spec.dashboards ?? []).map(dashboardJson), false],
    ["savedSearches", spec.savedSearches, false],
    ["alerts", spec.alerts, false],
  ]);
  const goEscapes: Record<string, string> = { "<": "\\u003c", ">": "\\u003e", "&": "\\u0026" };
  return `${JSON.stringify(doc, null, 2).replace(/[<>&]/g, (c) => goEscapes[c])}\n`;
}
