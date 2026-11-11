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

import { canonicalJson } from "@/services/controlPlane/observability/canonicalJson";
import type { AttributeFilter, Dashboard, Panel, Predicate, SeriesIntent } from "@/services/controlPlane/observability/types";

import { attributeClass, ENV_KEY, SERVICE_KEY } from "./attributes";
import { PALETTE_KEY_TOKENS } from "./charts/chartPalette";
import { parseFormula } from "./Dashboards/formula";

export const SPEC_VERSION = "forge.dashboards/v1";

/** Where a promoted dashboard lives in the user's repo. */
export const DASHBOARDS_DIR = "deploy/observability/dashboards";

export const PANEL_KINDS = ["line", "number", "table", "bar", "heatmap", "search", "sql", "postgres", "text", "funnel", "issues", "sessions"] as const;
export { DRAWN_KINDS, panelRunsQuery } from "@/services/controlPlane/observability/panelKinds";

/** The kinds read from the tenant's browser tables, not from series over a signal (C7). */
export const BROWSER_KINDS = ["funnel", "issues", "sessions"] as const;

export const SIGNALS = ["traces", "metrics", "logs"] as const;
export const AGGS = ["count", "count_distinct", "sum", "avg", "min", "max", "quantile", "last", "rate", "increase"] as const;
/** The aggregations that only mean something over a metric: a rate or an increase of a trace or log field is undefined. */
export const METRIC_ONLY_AGGS = ["rate", "increase", "min", "last"] as const;
export const OPS = ["eq", "neq", "in", "not_in", "prefix", "match", "exists", "gte"] as const;
export const UNITS = ["", "count", "ms", "bytes", "percent", "s"] as const;
export const METRIC_TYPES = ["sum", "gauge", "histogram", "exp_histogram", "summary"] as const;
/** The metric types a quantile is defined over: a summary stores fixed quantiles that cannot be merged across series. */
const QUANTILE_TYPES = ["histogram", "exp_histogram"] as const;
/** The metric types that count observations: a histogram or a summary. A gauge's points and a counter's increments are not observations. */
const OBSERVATION_TYPES = ["histogram", "exp_histogram", "summary"] as const;
/** The metric types rate and increase are defined over: a gauge has no growth. */
const GROWTH_TYPES = ["sum", "histogram", "exp_histogram", "summary"] as const;
/** The annotation sources forge knows; `releases` is the only one today. */
export const ANNOTATION_SOURCES = ["releases"] as const;

/**
 * The display styles the panel builder offers each kind, its default first. A
 * kind not listed is drawn one way. Every style here is in the builder's
 * fixture (editor-panels.json), which internal/dashspeccontract holds to the
 * forge this repo pins, so a style forge added joins it with the pin bump.
 */
export const DISPLAY_STYLES: Readonly<Partial<Record<string, readonly string[]>>> = {
  line: ["line", "area", "bars"],
  bar: ["bar", "pie", "donut"],
  number: ["stat", "gauge"],
  sql: ["line", "area", "bars", "funnel"],
};

/** The styles forge accepts (dashspec displayStyles): the builder's, and a sql heatmap the console draws. */
const ACCEPTED_STYLES: Readonly<Partial<Record<string, readonly string[]>>> = {
  ...DISPLAY_STYLES,
  sql: [...(DISPLAY_STYLES.sql ?? []), "heatmap"],
};

/** The styles that draw no figure: no sparkline, no series to colour apart (dashspec). */
const ONE_COLOUR_STYLES = ["funnel", "heatmap"];

/** forge's palette keys (dashspec ChartColors); a colour is named, never given as a value. */
const PALETTE_KEYS = Object.keys(PALETTE_KEY_TOKENS);
/** dashspec MaxColors and its key bound. */
const MAX_COLORS = 50;
const MAX_COLOR_NAME_BYTES = 200;
/** dashspec MaxSectionDescriptionBytes. */
const MAX_SECTION_DESCRIPTION_BYTES = 500;

/** dashspec MaxTopN. */
export const MAX_TOP_N = 50;

/** Which of Display's options mean something for `kind` drawn as `style` (dashspec validator.display). */
export function displayOptions(kind: string, style: string): { stack: boolean; horizontal: boolean; gauge: boolean; topN: boolean } {
  const timeSeries = kind === "line" || kind === "sql";
  return {
    stack: (timeSeries && (style === "area" || style === "bars")) || (kind === "bar" && style === "bar"),
    horizontal: kind === "bar" && style === "bar",
    gauge: kind === "number" && style === "gauge",
    topN: kind === "line" || (kind === "sql" && !ONE_COLOUR_STYLES.includes(style)) || (kind === "bar" && (style === "pie" || style === "donut")),
  };
}

/** forge's limits (internal/obsdash/validate.go). */
const MAX_SERIES = 10;
const MAX_PANELS = 200;
const MAX_LIMIT = 200;
const MAX_GRID_HEIGHT = 60;
const MIN_FUNNEL_STEPS = 2;
const MAX_FUNNEL_STEPS = 10;
const FUNNEL_WINDOW = /^[1-9][0-9]*[smhd]$/;
const ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.-]*$/;
/** dashspec ParseSpan: a sql panel's minStep. */
const SPAN = /^[1-9][0-9]{0,3}[smhd]$/;
/** dashspec MaxSQLBytes and dbquery.MaxSQLBytes: both statements' bound. */
const MAX_SQL_BYTES = 16 << 10;
const MAX_MARKDOWN_BYTES = 10_000;
const POSTGRES_DATABASE = /^[a-z0-9]([-a-z0-9_]*[a-z0-9])?$/;
const MAX_POSTGRES_NAME = 63;
const POSTGRES_DRAWS = ["", "auto", "line", "number", "table", "bar"];
/** The semantic fields that never hold a number, so a heatmap cannot bucket them (dashspec nonNumericFields). */
const NON_NUMERIC_FIELDS = new Set([SERVICE_KEY, ENV_KEY, "timestamp", "span.name", "span.kind", "span.status", "span.status_message", "log.severity", "log.body"]);
/** The kinds whose panel is a statement or prose: no series, filters or group-by. */
const SELF_CONTAINED_KINDS = ["sql", "postgres", "text"] as const;

const bytes = (text: string) => new TextEncoder().encode(text).length;

export const AGG_LABELS: Record<(typeof AGGS)[number], string> = {
  count: "count",
  count_distinct: "count distinct",
  sum: "sum",
  avg: "average",
  min: "min",
  max: "max",
  quantile: "quantile",
  last: "last value",
  rate: "rate (per second)",
  increase: "increase",
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

const SIGNAL_FIELDS: Record<string, { names: readonly string[]; prefix: string }> = {
  traces: { names: ["timestamp", "span.name", "span.kind", "span.status", "span.status_message", "span.duration"], prefix: "span.attr." },
  logs: { names: ["timestamp", "log.severity", "log.body"], prefix: "log.attr." },
  metrics: { names: [], prefix: "metric.attr." },
};

/** Whether `field` is a semantic field on `signal` (dashspec CheckField): the fields every signal has, a resource attribute, or the signal's own. */
export function fieldOnSignal(field: string, signal: string): boolean {
  const key = (prefix: string) => field.startsWith(prefix) && field.length > prefix.length && field.length - prefix.length <= 128;
  if (field === SERVICE_KEY || field === ENV_KEY || key("resource.attr.")) return true;
  const own = SIGNAL_FIELDS[signal];
  return !!own && (own.names.includes(field) || key(own.prefix));
}

const has = <T extends string>(list: readonly T[], value: unknown): value is T => typeof value === "string" && (list as readonly string[]).includes(value);

function checkPredicates(where: Predicate[] | undefined, at: string, problems: string[], signal: string) {
  (where ?? []).forEach((p, i) => {
    const here = `${at} filter ${i + 1}`;
    if (!p.field?.trim()) problems.push(`${here}: choose a field.`);
    else if (has(SIGNALS, signal) && !fieldOnSignal(p.field, signal)) problems.push(`${here}: "${p.field}" is not a field of ${signal}.`);
    if (!has(OPS, p.op)) problems.push(`${here}: "${p.op}" is not an operator.`);
    else if ((p.op === "in" || p.op === "not_in") && !(p.values ?? []).length) problems.push(`${here}: give at least one value.`);
    else if (p.op !== "exists" && p.op !== "in" && p.op !== "not_in" && !(p.value ?? "").trim()) problems.push(`${here}: give a value.`);
  });
}

/**
 * What is wrong with a panel, in words for the editor — the rules forge
 * applies to a dashboard file (internal/obsdash/validate.go), so a file that
 * passes here passes `forge obs dashboards check`. Whether the server can
 * compile a field it does not know is its own answer, shown on the panel.
 */
export function panelProblems(panel: Panel): string[] {
  const problems: string[] = [];
  if (!panel.id?.trim()) problems.push("The panel needs an id.");
  else if (!ID_PATTERN.test(panel.id)) problems.push(`"${panel.id}" is not a valid panel id (letters, digits, '-', '_' and '.').`);
  if (!panel.title?.trim()) problems.push("Give the panel a title.");
  if (!has(PANEL_KINDS, panel.kind)) problems.push(`"${panel.kind}" is not a panel kind.`);
  if (panel.unit !== undefined && !has(UNITS, panel.unit)) problems.push(`"${panel.unit}" is not a unit.`);
  if (panel.width !== undefined && panel.width !== 0 && (panel.width < 1 || panel.width > 24 || !Number.isInteger(panel.width))) problems.push("Width is 1 to 24 grid columns.");
  if (panel.height !== undefined && (panel.height < 0 || panel.height > MAX_GRID_HEIGHT || !Number.isInteger(panel.height))) problems.push(`Height is a whole number of rows up to ${MAX_GRID_HEIGHT}.`);
  for (const [key, kind, what] of OWN_FIELDS) if (panel.kind !== kind && present(panel[key])) problems.push(`Only a ${kind} panel takes ${what}.`);
  displayProblems(panel, problems);
  if (has(SELF_CONTAINED_KINDS, panel.kind)) {
    selfContainedProblems(panel, problems);
    return problems;
  }
  const browser = has(BROWSER_KINDS, panel.kind);
  if (!(browser && !panel.signal) && !has(SIGNALS, panel.signal)) problems.push(`"${panel.signal}" is not a signal.`);
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
      if (s.agg === "quantile" && !has(QUANTILE_TYPES, s.metricType)) problems.push(`${at}: a quantile over a metric needs a histogram.`);
      if ((s.agg === "rate" || s.agg === "increase") && !has(GROWTH_TYPES, s.metricType)) problems.push(`${at}: ${s.agg} needs a counter or a histogram: a gauge does not grow.`);
      if (s.agg === "count_distinct") problems.push(`${at}: count distinct is not defined over a metric; use sum, avg, min, max, last, rate, increase or quantile.`);
      if (s.agg === "count" && !has(OBSERVATION_TYPES, s.metricType)) problems.push(`${at}: count over a metric counts a histogram's observations; use sum, avg, min, max, last, rate or increase.`);
    } else {
      if (s.metricType) problems.push(`${at}: only a metrics series takes a metric type.`);
      if (s.agg !== "count" && !s.field?.trim()) problems.push(`${at}: ${s.agg} needs a field.`);
      if ((METRIC_ONLY_AGGS as readonly string[]).includes(s.agg)) problems.push(`${at}: ${s.agg} is defined for metrics; use max or avg.`);
    }
    checkPredicates(s.where, at, problems, panel.signal);
  });
  if (panel.ratio) {
    if (series.length !== 2) problems.push("A ratio divides the first series by the second: it needs exactly two.");
    if (panel.kind !== "line") problems.push("A ratio applies to a line chart.");
  }
  if ((panel.groupBy ?? []).length > 3) problems.push("Group by at most three fields.");
  for (const field of panel.groupBy ?? []) {
    if (!field.trim()) problems.push("Name every group-by field.");
    else if (has(SIGNALS, panel.signal) && !fieldOnSignal(field, panel.signal)) problems.push(`Group by: "${field}" is not a field of ${panel.signal}.`);
  }
  if (panel.formula?.trim()) {
    if (panel.kind !== "line" && panel.kind !== "number") problems.push(`A formula applies to a line or number panel, not a ${panel.kind} panel.`);
    if (panel.ratio) problems.push("A panel plots a formula or a ratio, not both (the ratio is the formula \"A / B\").");
    if (series.length > 0 && series.length <= MAX_SERIES) {
      try {
        parseFormula(panel.formula, series.length);
      } catch (e) {
        problems.push(`Formula: ${(e as Error).message}.`);
      }
    }
  }
  if (panel.kind === "bar" && !(panel.groupBy ?? []).length) problems.push("A bar panel needs a group-by: it draws one bar group per value.");
  if (panel.kind === "heatmap") heatmapProblems(panel, problems);
  if (panel.limit !== undefined && panel.limit !== 0) {
    if (panel.kind !== "table" && panel.kind !== "bar") problems.push("Only a table or bar panel takes a limit.");
    else if (!Number.isInteger(panel.limit) || panel.limit < 1 || panel.limit > MAX_LIMIT) problems.push(`The limit is a whole number of rows from 1 to ${MAX_LIMIT}.`);
  }
  checkPredicates(panel.where, "Panel", problems, panel.signal);
  return problems;
}

/** The fields one kind owns, and what the editor calls them: any other kind carrying one is refused. */
const OWN_FIELDS: readonly [keyof Panel, string, string][] = [
  ["markdown", "text", "Markdown"],
  ["sql", "sql", "a ClickHouse statement"],
  ["minStep", "sql", "a minimum step"],
  ["trendSql", "sql", "a sparkline statement"],
  ["postgres", "postgres", "a Postgres query"],
];

/**
 * Whether a value says anything once the console writes it: the console drops
 * undefined, empty strings, empty lists, false and empty objects before a
 * dashboard is saved (canonicalJson), as forge's own omitempty fields do, so
 * only what survives that is "present" to forge's validator.
 */
function present(value: unknown): boolean {
  return canonicalJson(value) !== undefined;
}

function heatmapProblems(panel: Panel, problems: string[]) {
  if ((panel.groupBy ?? []).length) problems.push("A heatmap takes no group-by: it draws one distribution.");
  if (panel.signal === "metrics") problems.push("A heatmap buckets individual events: its signal is traces or logs.");
  const series = panel.series ?? [];
  if (series.length !== 1) {
    problems.push(`A heatmap takes exactly one series, has ${series.length}.`);
    return;
  }
  const [s] = series;
  if (s.agg !== "count") problems.push("A heatmap counts events: its series is a count.");
  if (!s.field?.trim()) problems.push("Name the numeric field the heatmap buckets (span.duration, say).");
  else if (NON_NUMERIC_FIELDS.has(s.field)) problems.push(`"${s.field}" is not numeric: a heatmap buckets a numeric field such as span.duration.`);
}

/** The rules of the kinds that carry their own statement or prose (dashspec sqlPanel, postgresPanel, textPanel). */
function selfContainedProblems(panel: Panel, problems: string[]) {
  const kind = panel.kind;
  const refused: [string, unknown][] = [
    ["series", panel.series],
    ["ratio", panel.ratio],
    ["formula", panel.formula],
    ["limit", panel.limit],
    ["filters", panel.where],
    ["group-by", panel.groupBy],
    ["columns", panel.columns],
    ["funnel steps", panel.funnel],
    ["sessions filter", panel.hasError],
  ];
  if (kind !== "sql") refused.push(["signal", panel.signal]);
  if (kind === "text") refused.push(["unit", panel.unit]);
  for (const [what, value] of refused) if (present(value)) problems.push(`A ${kind} panel takes no ${what}.`);
  switch (kind) {
    case "text": {
      const markdown = panel.markdown ?? "";
      if (!markdown.trim()) problems.push("Write the panel's Markdown.");
      else if (bytes(markdown) > MAX_MARKDOWN_BYTES) problems.push(`The Markdown is ${bytes(markdown)} bytes, over the ${MAX_MARKDOWN_BYTES} a text panel may hold.`);
      return;
    }
    case "sql": {
      if (present(panel.signal) && !has(SIGNALS, panel.signal)) problems.push(`"${panel.signal}" is not a signal.`);
      statementProblems(panel.sql, problems);
      if (present(panel.minStep) && !SPAN.test(panel.minStep ?? "")) problems.push('The minimum step is a number and s, m, h or d ("1d").');
      if (present(panel.trendSql)) {
        if (!panel.trendSql?.trim()) problems.push("Write the sparkline's statement, or remove it.");
        else if (bytes(panel.trendSql) > MAX_SQL_BYTES) problems.push(`The sparkline's statement is ${bytes(panel.trendSql)} bytes, over the ${MAX_SQL_BYTES} a panel may run.`);
        if (ONE_COLOUR_STYLES.includes(panel.display?.style ?? "")) problems.push(`A sql panel drawn as a ${panel.display?.style} draws no figure, so it takes no sparkline.`);
      }
      return;
    }
    case "postgres": {
      const query = panel.postgres;
      if (!query) {
        problems.push("Choose the database to read, and write the statement.");
        return;
      }
      const database = query.database ?? "";
      if (!database.trim()) problems.push("Choose the database to read.");
      else if (database.length > MAX_POSTGRES_NAME || !POSTGRES_DATABASE.test(database)) problems.push(`"${database}" is not a database name: lowercase letters, digits, '-' and '_'.`);
      statementProblems(query.sql, problems);
      const draw = query.draw ?? "";
      if (!POSTGRES_DRAWS.includes(draw)) problems.push(`"${draw}" is not a way to draw a postgres panel (auto, line, number, table or bar).`);
      if (draw === "line" && !query.time?.trim()) problems.push("A line needs the time column: the column holding each row's timestamp.");
      const values = query.values ?? [];
      if (values.length > MAX_SERIES) problems.push(`Plot at most ${MAX_SERIES} value columns.`);
      if (draw === "number" && values.length > 1) problems.push("A number shows one value column.");
      if ((query.group ?? []).length > 3) problems.push("Split by at most three columns.");
      const columns = [...(query.time ? [query.time] : []), ...values, ...(query.group ?? [])];
      if (columns.some((name) => !name || name.length > MAX_POSTGRES_NAME)) problems.push(`A column name is 1 to ${MAX_POSTGRES_NAME} characters.`);
      return;
    }
  }
}

function statementProblems(sql: string | undefined, problems: string[]) {
  if (!sql?.trim()) problems.push("Write the panel's statement: one read-only SELECT.");
  else if (bytes(sql) > MAX_SQL_BYTES) problems.push(`The statement is ${bytes(sql)} bytes, over the ${MAX_SQL_BYTES} a panel may run.`);
}

/** The display rules (dashspec validator.display): a style the kind takes, and only the options that style uses. */
function displayProblems(panel: Panel, problems: string[]) {
  const display = panel.display;
  if (!display || !present(display)) return;
  const styles = ACCEPTED_STYLES[panel.kind];
  if (!styles) {
    problems.push(`A ${panel.kind} panel is drawn one way: only line, bar, number and sql panels take a display style.`);
    return;
  }
  if (present(display.style) && !styles.includes(display.style ?? "")) {
    problems.push(`"${display.style}" is not a style of a ${panel.kind} panel (${styles.join(", ")}).`);
    return;
  }
  const style = display.style || styles[0];
  const options = displayOptions(panel.kind, style);
  if (display.stack && !options.stack) problems.push(panel.kind === "bar" ? `A ${style} has no bars to stack.` : "Stack an area or bars style: stacked lines hide each series' own value.");
  if (display.horizontal && !options.horizontal) problems.push("Only a bar panel drawn as bars lies horizontal.");
  const hasMin = typeof display.min === "number";
  const hasMax = typeof display.max === "number";
  if (options.gauge) {
    if (!hasMax && panel.unit !== "percent") problems.push("A gauge needs a max, the top of its scale (a percent panel's scale is 0 to 1 without one).");
    else if (hasMax && hasMin && (display.min as number) >= (display.max as number)) problems.push("A gauge's min must be below its max.");
    else if (hasMax && !hasMin && (display.max as number) <= 0) problems.push("A gauge's max must be above its min (0 when unset).");
  } else if (hasMin || hasMax) problems.push("Only a gauge takes a min and a max.");
  if (display.topN !== undefined) {
    if (!Number.isInteger(display.topN) || display.topN < 1 || display.topN > MAX_TOP_N) problems.push(`Top N is a whole number of series from 1 to ${MAX_TOP_N}.`);
    else if (!options.topN) problems.push(`A ${style} ${panel.kind} panel has no series to fold into "Other"${panel.kind === "bar" ? " (a bar panel keeps its top groups with its limit)" : ""}.`);
  }
  if ((style === "pie" || style === "donut") && (panel.series ?? []).length !== 1) problems.push(`A ${style} draws one value per group: it takes exactly one series.`);
  colourProblems(display, style, problems);
}

/** The colour rules (dashspec validator.colors): palette keys, never values, and a colour per series only where series are drawn apart. */
function colourProblems(display: NonNullable<Panel["display"]>, style: string, problems: string[]) {
  const palette = PALETTE_KEYS.join(", ");
  if (present(display.color) && !has(PALETTE_KEYS, display.color)) problems.push(`"${display.color}" is not a palette colour (${palette}).`);
  const colors = display.colors;
  if (!colors || !present(colors)) return;
  if (ONE_COLOUR_STYLES.includes(style)) {
    problems.push(`A ${style} is drawn in one colour: set its colour, not one per series.`);
    return;
  }
  const names = Object.keys(colors);
  if (names.length > MAX_COLORS) problems.push(`Colour at most ${MAX_COLORS} series by name.`);
  for (const name of names) {
    if (!name.trim()) problems.push("A coloured series needs its name.");
    else if (bytes(name) > MAX_COLOR_NAME_BYTES) problems.push(`A series name is at most ${MAX_COLOR_NAME_BYTES} bytes.`);
    if (!has(PALETTE_KEYS, colors[name])) problems.push(`"${colors[name]}" is not a palette colour (${palette}).`);
  }
}

export function dashboardProblems(dashboard: Dashboard): string[] {
  const problems: string[] = [];
  if (!dashboard.id?.trim()) problems.push("The dashboard needs an id.");
  else if (!ID_PATTERN.test(dashboard.id)) problems.push(`"${dashboard.id}" is not a valid dashboard id.`);
  if (!dashboard.title?.trim()) problems.push("The dashboard needs a title.");
  if (present(dashboard.defaultCompare) && dashboard.defaultCompare !== "period" && dashboard.defaultCompare !== "release") {
    problems.push(`"${dashboard.defaultCompare}" is not a comparison: "period" or "release".`);
  }
  for (const section of dashboard.sections ?? []) {
    if (bytes(section.description ?? "") > MAX_SECTION_DESCRIPTION_BYTES) problems.push(`The description of section "${section.title}" is over ${MAX_SECTION_DESCRIPTION_BYTES} bytes.`);
  }
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
