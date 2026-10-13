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
 * release (contract K4). The file is ONE dashboard with a leading `"version":
 * "forge.dashboards/v1"` — exactly the per-dashboard file forge writes into a
 * release bundle (forge internal/obsdash/bundleset.go, `dashboardFile`) and the
 * shape `forge observe dashboards check` validates.
 *
 * ── SERIALIZED LIKE encoding/json ────────────────────────────────────────────
 *
 * Keys are written in the Go struct's field order and `omitempty` fields are
 * omitted when empty, so the file a user saves diffs cleanly against the one
 * forge generates (pinned by tests against forge's own output).
 */

import type {
  AttributeFilter,
  Dashboard,
  DashboardAnnotation,
  DashboardFilter,
  Panel,
  Predicate,
  SeriesIntent,
} from "@/services/controlPlane/observability/types";

import { attributeClass, ENV_KEY, SERVICE_KEY } from "./attributes";

export const SPEC_VERSION = "forge.dashboards/v1";

/** Where a saved dashboard lives in the user's repo. */
export const DASHBOARDS_DIR = "deploy/observability/dashboards";

export const PANEL_KINDS = ["line", "number", "table", "search"] as const;
export const SIGNALS = ["traces", "metrics", "logs"] as const;
export const AGGS = ["count", "count_distinct", "sum", "avg", "max", "quantile", "last"] as const;
export const OPS = ["eq", "neq", "in", "not_in", "prefix", "match", "exists", "gte"] as const;
export const UNITS = ["", "count", "ms", "bytes", "percent", "s"] as const;
export const METRIC_TYPES = ["sum", "gauge", "histogram"] as const;
/** The annotation sources forge knows; `releases` is the only one today. */
export const ANNOTATION_SOURCES = ["releases"] as const;

/** forge's limits (internal/obsdash/validate.go). */
const MAX_SERIES = 10;
const MAX_PANELS = 200;
const MAX_GRID_HEIGHT = 60;
const ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.-]*$/;

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

/** One dashboard file: the dashboard with the spec version it was written for. */
export type DashboardFile = Dashboard & { version: string };

/**
 * The dashboard as a file to commit. Environment, service and time range are
 * where the reader is LOOKING and are not baked in — a committed dashboard
 * ships to every environment its release is promoted to, and keeps its own
 * environment and service dropdowns. Its annotations (release markers) are
 * kept; `source` is the server's, never the file's.
 */
export function buildDashboardFile(input: SaveAsCodeInput): DashboardFile {
  const panels = input.dashboard.panels.map((panel) => {
    const where = [...(panel.where ?? []), ...input.filters.map((f) => predicateForFilter(f, panel.signal))];
    const aggregating = panel.kind !== "search";
    return {
      ...panel,
      where,
      groupBy: aggregating && input.groupBy.length ? input.groupBy.map((key) => fieldForKey(key, panel.signal)) : panel.groupBy,
    };
  });
  const { source: _source, ...dashboard } = input.dashboard;
  return { version: SPEC_VERSION, ...dashboard, id: input.name, title: input.title, panels };
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
  // Two projects in scope with the same id are served as `<project>/<id>`.
  const [owner, id] = dashboardId.includes("/") ? dashboardId.split("/", 2) : [project, dashboardId];
  return dashboardFileName(owner ? `${owner}-${id}` : id);
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
 * What is wrong with a panel, in words for the editor — the rules forge
 * applies to a dashboard file (internal/obsdash/validate.go), so a file that
 * passes here passes `forge observe dashboards check`. Whether the server can
 * compile a field it does not know is its own answer, shown on the panel.
 */
export function panelProblems(panel: Panel): string[] {
  const problems: string[] = [];
  if (!panel.id?.trim()) problems.push("The panel needs an id.");
  else if (!ID_PATTERN.test(panel.id)) problems.push(`"${panel.id}" is not a valid panel id (letters, digits, '-', '_' and '.').`);
  if (!panel.title?.trim()) problems.push("Give the panel a title.");
  if (!has(PANEL_KINDS, panel.kind)) problems.push(`"${panel.kind}" is not a panel kind.`);
  if (!has(SIGNALS, panel.signal)) problems.push(`"${panel.signal}" is not a signal.`);
  if (panel.unit !== undefined && !has(UNITS, panel.unit)) problems.push(`"${panel.unit}" is not a unit.`);
  const series = panel.series ?? [];
  if (panel.kind === "search") {
    if (series.length > 0) problems.push("A rows panel shows rows and takes no series: list its columns instead.");
    if (!(panel.columns ?? []).length) problems.push("A rows panel needs at least one column.");
  } else {
    if ((panel.columns ?? []).length) problems.push("Only a rows panel takes columns.");
    if (series.length === 0) problems.push("Add at least one series.");
    if (series.length > MAX_SERIES) problems.push(`A panel holds at most ${MAX_SERIES} series.`);
  }
  const labels = new Set<string>();
  series.forEach((s: SeriesIntent, i) => {
    const at = `Series ${i + 1}`;
    if (!s.label?.trim()) problems.push(`${at}: give it a label.`);
    else if (labels.has(s.label)) problems.push(`${at}: another series is already labelled "${s.label}".`);
    labels.add(s.label);
    if (!has(AGGS, s.agg)) problems.push(`${at}: "${s.agg}" is not an aggregation.`);
    if (s.agg === "quantile" && !(typeof s.quantile === "number" && s.quantile > 0 && s.quantile <= 1)) problems.push(`${at}: the quantile is a fraction, like 0.95.`);
    if (s.agg !== "quantile" && s.quantile !== undefined) problems.push(`${at}: only a quantile series takes a quantile.`);
    if (panel.signal === "metrics") {
      if (!s.field?.trim()) problems.push(`${at}: name the metric.`);
      if (!has(METRIC_TYPES, s.metricType)) problems.push(`${at}: say the metric's type (sum, gauge or histogram).`);
      if (s.agg === "quantile" && s.metricType !== "histogram") problems.push(`${at}: a quantile over a metric needs a histogram.`);
      if (s.agg === "count" || s.agg === "count_distinct") problems.push(`${at}: ${s.agg} is not defined over a metric; use sum, avg, max, last or quantile.`);
    } else {
      if (s.metricType) problems.push(`${at}: only a metrics series takes a metric type.`);
      if (s.agg !== "count" && !s.field?.trim()) problems.push(`${at}: ${s.agg} needs a field.`);
      if (s.agg === "last") problems.push(`${at}: last is defined for metrics; use max or avg.`);
    }
    checkPredicates(s.where, at, problems);
  });
  if (panel.ratio) {
    if (series.length !== 2) problems.push("A ratio divides the first series by the second: it needs exactly two.");
    if (panel.kind !== "line") problems.push("A ratio applies to a line chart.");
  }
  if ((panel.groupBy ?? []).length > 3) problems.push("Group by at most three fields.");
  if (panel.width !== undefined && panel.width !== 0 && (panel.width < 1 || panel.width > 24 || !Number.isInteger(panel.width))) problems.push("Width is 1 to 24 grid columns.");
  if (panel.height !== undefined && (panel.height < 0 || panel.height > MAX_GRID_HEIGHT || !Number.isInteger(panel.height))) problems.push(`Height is a whole number of rows up to ${MAX_GRID_HEIGHT}.`);
  checkPredicates(panel.where, "Panel", problems);
  return problems;
}

export function dashboardProblems(dashboard: Dashboard): string[] {
  const problems: string[] = [];
  if (!dashboard.id?.trim()) problems.push("The dashboard needs an id.");
  else if (!ID_PATTERN.test(dashboard.id)) problems.push(`"${dashboard.id}" is not a valid dashboard id.`);
  if (!dashboard.title?.trim()) problems.push("The dashboard needs a title.");
  const panels = dashboard.panels ?? [];
  if (panels.length === 0) problems.push("A dashboard needs at least one panel.");
  if (panels.length > MAX_PANELS) problems.push(`A dashboard holds at most ${MAX_PANELS} panels.`);
  const sources = new Set<string>();
  for (const a of dashboard.annotations ?? []) {
    if (!has(ANNOTATION_SOURCES, a.source)) problems.push(`"${a.source}" is not an annotation source.`);
    else if (sources.has(a.source)) problems.push(`The annotation source "${a.source}" is declared twice.`);
    sources.add(a.source);
  }
  const ids = new Set<string>();
  for (const panel of panels) {
    if (ids.has(panel.id)) problems.push(`Two panels share the id "${panel.id}".`);
    ids.add(panel.id);
    for (const problem of panelProblems(panel)) problems.push(`${panel.title || panel.id}: ${problem}`);
  }
  return problems;
}

export function dashboardFileProblems(file: DashboardFile): string[] {
  return [...(file.version !== SPEC_VERSION ? [`The version must be ${SPEC_VERSION}.`] : []), ...dashboardProblems(file)];
}

export function specProblems(spec: ObsdashSpec): string[] {
  return [...(spec.version !== SPEC_VERSION ? [`The version must be ${SPEC_VERSION}.`] : []), ...(spec.dashboards ?? []).flatMap(dashboardProblems)];
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

function annotationJson(a: DashboardAnnotation) {
  return ordered([
    ["source", a.source, false],
    ["label", a.label, true],
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

function dashboardEntries(d: Dashboard): [string, unknown, boolean][] {
  return [
    ["id", d.id, false],
    ["title", d.title, false],
    ["description", d.description, true],
    ["filters", (d.filters ?? []).map(filterJson), true],
    ["annotations", (d.annotations ?? []).map(annotationJson), true],
    ["panels", (d.panels ?? []).map(panelJson), false],
  ];
}

function dashboardJson(d: Dashboard) {
  return ordered(dashboardEntries(d));
}

/**
 * One dashboard file as forge writes it (`dashboardFile`: the version, then
 * the dashboard's own fields), two-space indented, ending in a newline.
 */
export function dashboardFileJson(file: DashboardFile): string {
  return goJson(ordered([["version", file.version, false], ...dashboardEntries(file)]));
}

function goJson(doc: { [key: string]: Json }): string {
  const goEscapes: Record<string, string> = { "<": "\\u003c", ">": "\\u003e", "&": "\\u0026" };
  return `${JSON.stringify(doc, null, 2).replace(/[<>&]/g, (c) => goEscapes[c])}\n`;
}

/**
 * A whole spec document as forge writes it (dashboard-spec_gen.json):
 * json.MarshalIndent with two spaces, then a newline; Go field order; `<` `>`
 * `&` escaped as encoding/json does.
 */
export function specJson(spec: ObsdashSpec): string {
  const doc = ordered([
    ["version", spec.version, false],
    ["project", spec.project, false],
    ["dashboards", (spec.dashboards ?? []).map(dashboardJson), false],
    ["savedSearches", spec.savedSearches, false],
  ]);
  return goJson(doc);
}
