// Copyright (c) 2025 Reliant Labs

/**
 * forge's dashboard spec (obsdash, `forge.dashboards/v1`; forge
 * pkg/dashspec) on the console side: its vocabulary — kinds, signals,
 * aggregations, operators, units — for the panel builder, and the rules forge
 * applies to a panel, in words the builder can show before anything is saved.
 *
 * The console never writes a dashboard file of its own: a workspace
 * dashboard is stored by control-plane (DashboardService), which validates it
 * with forge's own rules and keeps forge's canonical form, and "Promote to
 * code" returns the file exactly as forge writes it. These checks are the
 * early, friendly copy of those rules — the server's answer is the final one.
 */

import type { AttributeFilter, Dashboard, Panel, Predicate, SeriesIntent } from "@/services/controlPlane/observability/types";

import { attributeClass, ENV_KEY, SERVICE_KEY } from "./attributes";

export const SPEC_VERSION = "forge.dashboards/v1";

/** Where a promoted dashboard lives in the user's repo. */
export const DASHBOARDS_DIR = "deploy/observability/dashboards";

export const PANEL_KINDS = ["line", "number", "table", "search", "funnel", "issues", "sessions"] as const;
/** The kinds read from the tenant's browser tables, not from series over a signal (C7). */
export const BROWSER_KINDS = ["funnel", "issues", "sessions"] as const;

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
const MIN_FUNNEL_STEPS = 2;
const MAX_FUNNEL_STEPS = 10;
const FUNNEL_WINDOW = /^[1-9][0-9]*[smhd]$/;
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
  const browser = has(BROWSER_KINDS, panel.kind);
  if (!(browser && !panel.signal) && !has(SIGNALS, panel.signal)) problems.push(`"${panel.signal}" is not a signal.`);
  if (panel.unit !== undefined && !has(UNITS, panel.unit)) problems.push(`"${panel.unit}" is not a unit.`);
  const series = panel.series ?? [];
  if (browser) {
    if (series.length > 0) problems.push(`A ${panel.kind} panel takes no series.`);
    if (panel.ratio || (panel.groupBy ?? []).length || (panel.columns ?? []).length) problems.push(`A ${panel.kind} panel takes no ratio, group-by or columns.`);
    if (panel.signal && panel.signal !== "traces") problems.push(`A ${panel.kind} panel reads browser telemetry: its signal is traces.`);
  }
  if (panel.kind === "funnel") {
    const steps = panel.funnel?.steps ?? [];
    if (steps.length < MIN_FUNNEL_STEPS || steps.length > MAX_FUNNEL_STEPS) problems.push(`A funnel has ${MIN_FUNNEL_STEPS} to ${MAX_FUNNEL_STEPS} steps.`);
    if (steps.some((step) => !step.trim())) problems.push("Name every funnel step.");
    if (new Set(steps).size !== steps.length) problems.push("Each funnel step is a different event.");
    if (panel.funnel?.window && !FUNNEL_WINDOW.test(panel.funnel.window)) problems.push('The funnel window is a number and s, m, h or d ("1h", "7d").');
    if (panel.funnel?.by && panel.funnel.by !== "session" && panel.funnel.by !== "user") problems.push('A funnel counts per "session" or per "user".');
  } else if (panel.funnel) problems.push("Only a funnel panel takes funnel steps.");
  if (panel.hasError && panel.kind !== "sessions") problems.push("Only a sessions panel narrows to sessions with errors.");
  if (panel.kind === "search") {
    if (series.length > 0) problems.push("A rows panel shows rows and takes no series: list its columns instead.");
    if (!(panel.columns ?? []).length) problems.push("A rows panel needs at least one column.");
  } else if (!browser) {
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
