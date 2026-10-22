// Copyright (c) 2025 Reliant Labs

/**
 * "Create alert from this panel": a dashboard panel turned into the KCL alert
 * rule that watches the same number, for the user to paste into their repo.
 *
 * Alerts stay declarative: the console stores nothing. A rule is a
 * `forge.AlertRule` in the environment's `deploy/kcl/<env>/main.k`, and reaches
 * the control plane with the next deploy. This module is the pure part — panel
 * and a few choices in, SQL and KCL out — so the dialog is only a form over it.
 *
 * The SQL is the panel's own query, compiled for an alert instead of a chart.
 * It follows control-plane's panel compiler (internal/obsquery/panel.go) field
 * by field, so the number an alert watches is the number the panel draws:
 * the same field vocabulary, the same predicates, the same aggregates, and the
 * same sampling weight on trace counts. What differs is the shape. A panel
 * returns buckets over the dashboard's range; a rule returns one row per
 * firing instance over a look-back window ending now — the non-`value` columns
 * are the instance's labels, and a row exists only when the condition holds.
 *
 * Not every panel is a number to compare with a threshold. A heatmap is a
 * distribution; a funnel, an issue list, a session list and a rows panel are
 * lists. They are refused here, with what to build instead.
 */

import type { DeclaredAlertRule } from "@/services/controlPlane/observability/alertingTypes";
import type { Panel, Predicate, SeriesIntent } from "@/services/controlPlane/observability/types";

import { alertRuleKcl, kclPath, ruleAsCode } from "../alertsAsCode";
import { attributeClass } from "../attributes";
import { alertExprProblem, bindPanelSql, chString, ENV_ID_PARAM, intervalOf } from "./alertExpr";
import { formulaSql, parseFormula } from "./formula";

// ── Choices ──────────────────────────────────────────────────────────────────

export const COMPARATORS = [
  { value: "above", label: "above", sql: ">" },
  { value: "above_or_equal", label: "at or above", sql: ">=" },
  { value: "below", label: "below", sql: "<" },
  { value: "below_or_equal", label: "at or below", sql: "<=" },
] as const;
export type Comparator = (typeof COMPARATORS)[number]["value"];

export const SEVERITIES = ["critical", "warning", "info"] as const;
export type AlertSeverity = (typeof SEVERITIES)[number];

/** What the form edits. Free text stays text until it is checked. */
export interface AlertChoices {
  /** The rule's name: metric-style, unique within the environment. */
  name: string;
  comparator: Comparator;
  /** In the panel's unit: a percent panel's 5 means 5%. */
  threshold: string;
  /** How far back the query looks: "5m", "1h". */
  window: string;
  /** How long the condition must hold before it fires: "5m". */
  hold: string;
  severity: AlertSeverity;
  /** Which of the panel's series to watch, when it has several and plots none combined. */
  series: number;
}

/** Where the dashboard was looking when the reader asked for the alert. */
export interface AlertScope {
  /** The environment the rule is declared in: its KCL file, and the env its data is scoped to. */
  envName: string;
  /** The dashboard's service filter; none is every service. */
  services: readonly string[];
  dashboardTitle?: string;
}

/** The alert rules the environment already declares, for the name check and the paste hint. */
export interface DeclaredRules {
  /** False when the environment has recorded nothing yet, so nothing is known about its rules. */
  recorded: boolean;
  /** Names of the rules the project itself declares (not forge's generated defaults). */
  own: readonly string[];
  /** Every rule name in the environment, defaults included. */
  all: readonly string[];
}

// ── Limits ───────────────────────────────────────────────────────────────────

/** The evaluator's floor for how often a rule runs (forge.AlertRule.interval). */
const MIN_WINDOW_SECONDS = 60;
/** Traces and logs are kept 7 days; a longer look-back reads nothing more. */
const MAX_WINDOW_SECONDS = 7 * 24 * 3600;
/** forge alerting.DefaultLimits().MaxInstancesPerRule: more rows than this is an error, not a cut. */
export const MAX_INSTANCES = 200;
const DURATION_GROUPS = /^([0-9]+[smh])+$/;
const ALERT_NAME = /^[a-zA-Z_][a-zA-Z0-9_]*$/;
const MAX_NAME_LEN = 128;

const UNIT_SECONDS = { s: 1, m: 60, h: 3600 } as const;

/** "1h30m" → 5400; null when it is not a duration forge's KCL accepts. */
export function durationSeconds(text: string): number | null {
  if (!DURATION_GROUPS.test(text)) return null;
  let total = 0;
  for (const [, n, unit] of text.matchAll(/([0-9]+)([smh])/g)) total += Number(n) * UNIT_SECONDS[unit as keyof typeof UNIT_SECONDS];
  return total;
}

// ── Results ──────────────────────────────────────────────────────────────────

/** A panel that cannot become an alert, and what to do instead. */
export interface Unsupported {
  status: "unsupported";
  reason: string;
  instead?: string;
}

export interface AlertSnippet {
  status: "ok";
  /** The SQL, null when a choice is invalid so none can be written yet. */
  expr: string | null;
  /** One entry per field the reader must fix, keyed by `AlertChoices`. */
  fieldProblems: Partial<Record<keyof AlertChoices, string>>;
  /** Why the control plane would refuse the expr; null when it would accept it. */
  exprProblem: string | null;
  /** The rule on its own, `forge.AlertRule { … }`, and wrapped in the env's observability block. */
  rule: string | null;
  block: string | null;
  /** The file to edit. */
  path: string;
  /** What the query does that the reader should know before trusting it. */
  notes: string[];
  /** Things worth a second look that do not block copying. */
  warnings: string[];
  /** Ready to copy: every choice valid and the expr acceptable. */
  ready: boolean;
}

export type AlertSnippetResult = Unsupported | AlertSnippet;

class Refused extends Error {
  readonly instead?: string;

  constructor(message: string, instead?: string) {
    super(message);
    this.instead = instead;
  }
}

// ── Field resolution (control-plane internal/obsquery/panel.go) ──────────────

type Signal = "traces" | "logs" | "metrics";

interface FieldRef {
  /** A String expression, or a number when `num`. */
  expr: string;
  num?: boolean;
  /** For attribute-backed fields: the map column and the key, for `exists`. */
  mapCol?: string;
  key?: string;
  special?: "sev" | "kind" | "status";
}

const MAX_ATTR_KEY = 128;
const MAX_FIELD = 160;

function attr(col: string, field: string, prefix: string): FieldRef {
  const key = field.slice(prefix.length);
  if (!key || key.length > MAX_ATTR_KEY) throw new Refused(`"${field}" needs an attribute key of 1 to ${MAX_ATTR_KEY} characters.`);
  return { expr: `${col}[${chString(key)}]`, mapCol: col, key: chString(key) };
}

function resolve(signal: Signal, field: string): FieldRef {
  if (field.length > MAX_FIELD) throw new Refused("A field name is too long.");
  if (field === "service.name") return { expr: "ServiceName" };
  if (field === "deployment.environment.name") return attr("ResourceAttributes", field, "");
  if (field.startsWith("resource.attr.")) return attr("ResourceAttributes", field, "resource.attr.");
  if (signal === "traces") {
    switch (field) {
      case "span.name":
        return { expr: "SpanName" };
      case "span.kind":
        return { expr: "lower(replaceOne(SpanKind, 'SPAN_KIND_', ''))", special: "kind" };
      case "span.status":
        return { expr: "lower(replaceOne(StatusCode, 'STATUS_CODE_', ''))", special: "status" };
      case "span.status_message":
        return { expr: "StatusMessage" };
      case "span.duration":
        return { expr: "(Duration / 1000000)", num: true };
    }
    if (field.startsWith("span.attr.")) return attr("SpanAttributes", field, "span.attr.");
  } else if (signal === "logs") {
    switch (field) {
      case "log.severity":
        return { expr: "upper(SeverityText)", special: "sev" };
      case "log.body":
        return { expr: "Body" };
    }
    if (field.startsWith("log.attr.")) return attr("LogAttributes", field, "log.attr.");
  } else if (field.startsWith("metric.attr.")) {
    return attr("Attributes", field, "metric.attr.");
  }
  throw new Refused(`"${field}" is not a field of a ${signal} panel.`);
}

const numeric = (ref: FieldRef) => (ref.num ? ref.expr : `toFloat64OrNull(${ref.expr})`);

// Stored SeverityText spellings per level, trace → fatal (control-plane logs.go).
const SEVERITY_NAMES: readonly (readonly string[])[] = [["TRACE"], ["DEBUG"], ["INFO", "NOTICE"], ["WARN", "WARNING"], ["ERROR", "ERR"], ["FATAL", "CRITICAL", "PANIC"]];

function severityLevel(name: string): number {
  const upper = name.toUpperCase();
  const level = SEVERITY_NAMES.findIndex((names) => names.includes(upper));
  if (level < 0) throw new Refused(`"${name}" is not a severity (trace, debug, info, warn, error or fatal).`);
  return level;
}

const list = (values: readonly string[]) => values.map(chString).join(", ");

function specialCond(ref: FieldRef, p: Predicate): string {
  if (ref.special === "sev") {
    if (p.op === "gte") return `${ref.expr} IN (${list(SEVERITY_NAMES.slice(severityLevel(p.value ?? "")).flat())})`;
    const values = p.op === "eq" || p.op === "neq" ? [p.value ?? ""] : (p.values ?? []);
    const spellings = values.flatMap((v) => SEVERITY_NAMES[severityLevel(v)]);
    switch (p.op) {
      case "eq":
      case "in":
        return `${ref.expr} IN (${list(spellings)})`;
      case "neq":
      case "not_in":
        return `${ref.expr} NOT IN (${list(spellings)})`;
    }
    throw new Refused("log.severity supports is, is not, is one of, is none of and at least.");
  }
  const lower = (v: string) => v.toLowerCase();
  switch (p.op) {
    case "eq":
      return `${ref.expr} = ${chString(lower(p.value ?? ""))}`;
    case "neq":
      return `${ref.expr} != ${chString(lower(p.value ?? ""))}`;
    case "in":
    case "not_in":
      return `${ref.expr} ${p.op === "not_in" ? "NOT " : ""}IN (${list((p.values ?? []).map(lower))})`;
  }
  throw new Refused(`A filter on the span's ${ref.special} supports is, is not, is one of and is none of.`);
}

/** One filter as a SQL condition. */
function cond(signal: Signal, p: Predicate): string {
  const ref = resolve(signal, p.field);
  if (ref.special) return specialCond(ref, p);
  const value = p.value ?? "";
  switch (p.op) {
    case "exists":
      return ref.mapCol ? `mapContains(${ref.mapCol}, ${ref.key})` : ref.num ? "1" : `${ref.expr} != ''`;
    case "gte": {
      const n = Number(value);
      if (!value.trim() || !Number.isFinite(n)) throw new Refused(`"at least" needs a number, not "${value}".`);
      return `${numeric(ref)} >= ${n}`;
    }
  }
  if (ref.num) {
    const n = Number(value);
    if (!value.trim() || !Number.isFinite(n) || (p.op !== "eq" && p.op !== "neq")) throw new Refused(`${p.field} supports is, is not and at least, with a number.`);
    return `${ref.expr} ${p.op === "neq" ? "!=" : "="} ${n}`;
  }
  switch (p.op) {
    case "eq":
      return `${ref.expr} = ${chString(value)}`;
    case "neq":
      return `${ref.expr} != ${chString(value)}`;
    case "in":
    case "not_in":
      if (!(p.values ?? []).length) throw new Refused(`A filter on ${p.field} needs at least one value.`);
      return `${ref.expr} ${p.op === "not_in" ? "NOT " : ""}IN (${list(p.values ?? [])})`;
    case "prefix":
      return `startsWith(${ref.expr}, ${chString(value)})`;
    case "match":
      return `match(${ref.expr}, ${chString(value)})`;
  }
  throw new Refused(`"${p.op}" is not a filter operator.`);
}

const conds = (signal: Signal, filters: readonly Predicate[] | undefined) => (filters ?? []).map((p) => cond(signal, p));

/** A group-by key as a String expression: the panel's own vocabulary first, then the console's attribute allowlist. */
function groupExpr(signal: Signal, key: string): string {
  const own = (() => {
    try {
      return resolve(signal, key);
    } catch {
      return null;
    }
  })();
  if (own) return own.expr;
  const cls = attributeClass(key);
  if (!cls) throw new Refused(`"${key}" is not something a panel can group by.`);
  if (key === "status" && signal === "traces") return "lower(replaceOne(StatusCode, 'STATUS_CODE_', ''))";
  const k = chString(key);
  if (cls === "resource") return `ResourceAttributes[${k}]`;
  const col = signal === "logs" ? "LogAttributes" : signal === "metrics" ? "Attributes" : "SpanAttributes";
  return `if(mapContains(${col}, ${k}), ${col}[${k}], ResourceAttributes[${k}])`;
}

/** A column name for a group-by key, safe as a label: `span.attr.http.route` → `http_route`. */
export function labelName(key: string, taken: ReadonlySet<string>): string {
  if (key === "service.name") return uniqueLabel("service", taken);
  const bare = key.replace(/^(?:resource|span|log|metric)\.attr\./, "").replace(/^(?:span|log)\./, "");
  const clean = bare.replace(/[^A-Za-z0-9_]/g, "_").replace(/^_+|_+$/g, "") || "group";
  return uniqueLabel(/^[0-9]/.test(clean) ? `_${clean}` : clean, taken);
}

// Names the query itself uses (the sample column, the sampling-weight aliases), and the evaluator's own labels.
const RESERVED_LABELS = new Set(["value", "w", "th", "v", "alertname"]);

function uniqueLabel(name: string, taken: ReadonlySet<string>): string {
  let candidate = RESERVED_LABELS.has(name.toLowerCase()) || /^reliant_/i.test(name) ? `${name}_` : name;
  for (let i = 2; taken.has(candidate); i++) candidate = `${name}_${i}`;
  return candidate;
}

// ── Aggregates ───────────────────────────────────────────────────────────────

// Trace sampling (control-plane internal/obsquery/weight.go). The hosted gateway
// keeps every error trace and a fraction of the rest, and writes the kept
// fraction into the span's tracestate, so a stored span stands for 1/P spans.
// Counts, ratios and percentiles over spans are weighted by it; without the
// weight an error ratio reads about tenfold too high. `;` is written \x3B
// because forge.AlertRule refuses a semicolon anywhere in expr.
const SPAN_THRESHOLD = "extract(TraceState, '(?:^|,)ot=(?:[^\\x3B,]*\\x3B)*th:([0-9a-f]+)(?:[\\x3B,]|$)')";
const SPAN_WEIGHT = [
  `WITH ${SPAN_THRESHOLD} AS th,`,
  "     if(th = '', 1.0, round(least(1000000.0, 1 / greatest(1 - reinterpretAsUInt64(reverse(unhex(concat('00', rightPad(left(th, 14), 14, '0'))))) / 72057594037927936, 0.000001)), 6)) AS w",
].join("\n");
const WEIGHT_INT = "toUInt64(round(w))";

function aggregate(signal: Signal, s: SeriesIntent): string {
  const sc = conds(signal, s.where);
  const c = sc.length ? sc.join(" AND ") : "1";
  const ref = s.agg === "count" ? null : resolve(signal, s.field ?? "");
  const q = s.quantile ?? 0;
  const num = ref ? numeric(ref) : "";
  let e: string;
  if (signal === "traces") {
    switch (s.agg) {
      case "count":
        e = `sumIf(w, ${c})`;
        break;
      case "count_distinct":
        e = `uniqExactIf(toString(${ref!.expr}), ${c})`;
        break;
      case "sum":
        e = `sumIf(${num} * w, ${c})`;
        break;
      case "avg": {
        const nn = `${c} AND isNotNull(${num})`;
        e = `sumIf(${num} * w, ${nn}) / nullIf(sumIf(w, ${nn}), 0)`;
        break;
      }
      case "max":
        e = `maxIf(${num}, ${c})`;
        break;
      case "quantile":
        e = `quantileExactWeightedIf(${q})(${num}, ${WEIGHT_INT}, ${c})`;
        break;
      case "last":
        e = `argMaxIf(${num}, Timestamp, ${c})`;
        break;
      default:
        throw new Refused(`"${s.agg}" is not an aggregation.`);
    }
  } else {
    switch (s.agg) {
      case "count":
        e = `countIf(${c})`;
        break;
      case "count_distinct":
        e = `uniqExactIf(toString(${ref!.expr}), ${c})`;
        break;
      case "sum":
        e = `sumIf(${num}, ${c})`;
        break;
      case "avg":
        e = `avgIf(${num}, ${c})`;
        break;
      case "max":
        e = `maxIf(${num}, ${c})`;
        break;
      case "quantile":
        e = `quantileIf(${q})(${num}, ${c})`;
        break;
      case "last":
        e = `argMaxIf(${num}, Timestamp, ${c})`;
        break;
      default:
        throw new Refused(`"${s.agg}" is not an aggregation.`);
    }
  }
  return `ifNull(toFloat64(${e}), 0)`;
}

// ── Building the SQL ─────────────────────────────────────────────────────────

const TABLES: Record<Signal, string> = { traces: "otel_traces", logs: "otel_logs", metrics: "otel_metrics_gauge" };
const TIME_COLUMN: Record<Signal, string> = { traces: "Timestamp", logs: "Timestamp", metrics: "TimeUnix" };

function indent(lines: readonly string[], by = "  "): string {
  return lines.map((line) => by + line).join("\n");
}

interface Shape {
  signal: Signal;
  /** The conditions every row must meet: look-back window, environment, services, panel filters. */
  where: string[];
  /** `[expression, label]` for each group-by key. */
  groups: [string, string][];
}

function shapeOf(panel: Panel, signal: Signal, windowSeconds: number, scope: AlertScope): Shape {
  const where = [
    `${TIME_COLUMN[signal]} >= now() - INTERVAL ${intervalOf(windowSeconds)}`,
    `ResourceAttributes['reliant.env.id'] = ${ENV_ID_PARAM}`,
  ];
  if (scope.services.length) where.push(`ServiceName IN (${list(scope.services)})`);
  where.push(...conds(signal, panel.where));
  const taken = new Set<string>();
  const groups = (panel.groupBy ?? []).map((key): [string, string] => {
    const label = labelName(key, taken);
    taken.add(label);
    return [groupExpr(signal, key), label];
  });
  return { signal, where, groups };
}

function havingClause(comparator: Comparator, threshold: number): string {
  const sql = COMPARATORS.find((c) => c.value === comparator)!.sql;
  return `${sql} ${threshold}`;
}

/** Orders the rows the evaluator will keep when more fire than it allows. */
function order(comparator: Comparator): string {
  return comparator.startsWith("below") ? "ASC" : "DESC";
}

function select(shape: Shape, valueSql: string, having: string, comparator: Comparator): string {
  const labels = shape.groups.map(([expr, label]) => `toString(${expr}) AS ${label}`);
  const lines = [`SELECT ${[...labels, `${valueSql} AS value`].join(",\n       ")}`, `FROM ${TABLES[shape.signal]}`, "WHERE " + shape.where.join("\n  AND ")];
  if (shape.groups.length) lines.push(`GROUP BY ${shape.groups.map(([, label]) => label).join(", ")}`);
  lines.push(`HAVING value ${having}`);
  if (shape.groups.length) lines.push(`ORDER BY value ${order(comparator)}`, `LIMIT ${MAX_INSTANCES}`);
  return lines.join("\n");
}

function seriesNumber(panel: Panel, signal: Signal): { valueSql: string; what: string } {
  const series = panel.series ?? [];
  if (series.length === 0) throw new Refused("The panel has no series.");
  if (panel.ratio) {
    if (series.length !== 2) throw new Refused("A ratio divides the first series by the second: it needs exactly two.");
    const tree = parseFormula("A / B", 2);
    return { valueSql: formulaSql(tree, (i) => aggregate(signal, series[i])), what: `${series[0].label} / ${series[1].label}` };
  }
  if (panel.formula) {
    let tree;
    try {
      tree = parseFormula(panel.formula, series.length);
    } catch (error) {
      throw new Refused(`The panel's formula is not valid: ${(error as Error).message}.`);
    }
    return { valueSql: formulaSql(tree, (i) => aggregate(signal, series[i])), what: panel.formula };
  }
  return { valueSql: "", what: "" };
}

function builderSql(panel: Panel, choices: AlertChoices, windowSeconds: number, threshold: number, scope: AlertScope, notes: string[]): string {
  const signal = panel.signal as Signal;
  const shape = shapeOf(panel, signal, windowSeconds, scope);
  const series = panel.series ?? [];
  let { valueSql, what } = seriesNumber(panel, signal);
  if (!valueSql) {
    const chosen = series[Math.min(Math.max(choices.series, 0), series.length - 1)];
    if (!chosen) throw new Refused("The panel has no series.");
    valueSql = aggregate(signal, chosen);
    what = chosen.label;
  }
  notes.push(`Watches ${what}${shape.groups.length ? `, per ${shape.groups.map(([, label]) => label).join(" and ")} (one alert for each)` : ""}.`);
  const sql = select(shape, valueSql, havingClause(choices.comparator, threshold), choices.comparator);
  if (signal === "traces") {
    notes.push("Span counts are weighted by their sampling threshold, as the panel weights them: the gateway keeps every error trace but only a share of the rest.");
    return `${SPAN_WEIGHT}\n${sql}`;
  }
  return sql;
}

const GAUGE_AGGS: Record<string, string> = { avg: "avg", last: "avg", max: "max", sum: "sum" };

function metricsSql(panel: Panel, choices: AlertChoices, windowSeconds: number, threshold: number, scope: AlertScope, notes: string[]): string {
  const series = panel.series ?? [];
  if (panel.ratio || panel.formula) {
    throw new Refused("A ratio or formula over metrics joins two metrics, which a single alert query cannot do from a panel.", "Alert on one of the metrics, or write the rule by hand.");
  }
  const s = series[Math.min(Math.max(choices.series, 0), series.length - 1)];
  if (!s) throw new Refused("The panel has no series.");
  if (s.metricType !== "gauge") {
    throw new Refused(
      s.metricType === "histogram"
        ? `${s.field} is a histogram metric: its percentiles come from differences between exports, which a panel hides and an alert has to spell out.`
        : `${s.field} is a counter (a sum metric): its rate or increase comes from differences between exports, which a panel hides and an alert has to spell out.`,
      "Forge's generated default rules do this for request counts and latency (deploy/observability/alert-defaults_gen.json): copy one and change the metric. A gauge metric converts directly."
    );
  }
  const fn = GAUGE_AGGS[s.agg];
  if (!fn) throw new Refused(`A gauge cannot be aggregated with "${s.agg}": use average, max, sum or last value.`);
  const shape = shapeOf(panel, "metrics", windowSeconds, scope);
  const sc = conds("metrics", s.where);
  const where = [`MetricName = ${chString(s.field ?? "")}`, ...shape.where, ...sc];
  const names = shape.groups.map(([, label]) => label);
  const inner = [
    `SELECT ${[...shape.groups.map(([expr, label]) => `toString(${expr}) AS ${label}`), "argMax(Value, TimeUnix) AS v"].join(",\n       ")}`,
    `FROM ${TABLES.metrics}`,
    "WHERE " + where.join("\n  AND "),
    `GROUP BY ${[...names, "cityHash64(ResourceAttributes, Attributes)"].join(", ")}`,
  ].join("\n");
  const lines = [`SELECT ${[...names, `${fn}(v) AS value`].join(", ")}`, "FROM (", indent(inner.split("\n")), ")"];
  if (names.length) lines.push(`GROUP BY ${names.join(", ")}`);
  lines.push(`HAVING value ${havingClause(choices.comparator, threshold)}`);
  if (names.length) lines.push(`ORDER BY value ${order(choices.comparator)}`, `LIMIT ${MAX_INSTANCES}`);
  notes.push(`Watches ${s.label}: the latest value of each series in the window, then the ${fn} across series${names.length ? `, per ${names.join(" and ")} (one alert for each)` : ""}.`);
  return lines.join("\n");
}

function sqlPanelSql(panel: Panel, choices: AlertChoices, windowSeconds: number, threshold: number, scope: AlertScope, notes: string[], warnings: string[]): string {
  const raw = (panel.sql ?? "").trim().replace(/;\s*$/, "");
  if (!raw) throw new Refused("The panel has no SQL.");
  const bound = bindPanelSql(raw, { windowSeconds, envName: scope.envName, services: scope.services });
  if (bound.unbound.length) {
    throw new Refused(
      `The query uses ${bound.unbound.join(", ")}, which an alert rule is not given.`,
      "An alert rule is given the look-back window, the environment and the services; replace the parameter with a literal."
    );
  }
  if (!/\bvalue\b/i.test(raw)) {
    warnings.push("The query has no column named value. A rule reads its number from a column called value and turns every other column into a label: alias the column you want to compare, `… AS value`.");
  }
  if (/\{(?:from|to|step)\s*:/i.test(raw)) {
    notes.push(`{from}, {to} and {step} become the last ${choices.window} ending now, so a query that returns a row per time bucket returns one per bucket here: narrow it to the latest.`);
  }
  notes.push("The panel's query is wrapped so a row survives only when its value passes the threshold; every row left is an alert.");
  return [`SELECT *`, `FROM (`, indent(bound.sql.split("\n")), `)`, `WHERE value ${havingClause(choices.comparator, threshold)}`, `ORDER BY value ${order(choices.comparator)}`, `LIMIT ${MAX_INSTANCES}`].join("\n");
}

// ── Which panels are alertable ───────────────────────────────────────────────

const BUILDER_KINDS = new Set(["line", "number", "table", "bar"]);

/** Why a panel cannot become an alert, or null when it can. */
export function unsupportedReason(panel: Panel): Unsupported | null {
  const kind = panel.kind;
  if (kind === "heatmap") {
    const field = panel.series?.[0]?.field;
    return {
      status: "unsupported",
      reason: "A heatmap shows how values are distributed over time: a count for every value bucket, not one number to compare with a threshold.",
      instead: field
        ? `Add a Stat or Time series panel with a p95 (or max) series over ${field} and create the alert from that.`
        : "Add a Stat or Time series panel with a p95 (or max) series over the same field and create the alert from that.",
    };
  }
  if (kind === "funnel") {
    return {
      status: "unsupported",
      reason: "A funnel is a conversion across ordered steps, counted per session or per user: a list of steps, not one number.",
      instead: "Alert on the count of one step's event with a Stat panel over the same filters.",
    };
  }
  if (kind === "issues" || kind === "sessions") {
    return {
      status: "unsupported",
      reason: `${kind === "issues" ? "An issues" : "A sessions"} panel lists ${kind === "issues" ? "grouped errors" : "browser sessions"}: rows to read, not a number to watch.`,
      instead: "Alert on how many there are: a Stat panel counting the same errors, or sessions with an error.",
    };
  }
  if (kind === "search") {
    return {
      status: "unsupported",
      reason: "A rows panel lists individual log lines or spans: rows to read, not a number to watch.",
      instead: "Alert on how many rows match: a Stat panel with a count series and the same filters.",
    };
  }
  if (kind !== "sql" && !BUILDER_KINDS.has(kind)) {
    return { status: "unsupported", reason: `"${kind}" is not a panel kind an alert can be made from.` };
  }
  if (kind !== "sql" && !["traces", "logs", "metrics"].includes(panel.signal)) {
    return { status: "unsupported", reason: `"${panel.signal}" is not a signal: choose traces, logs or metrics first.` };
  }
  return null;
}

// ── Defaults and choices ─────────────────────────────────────────────────────

const PASCAL_WORD = /[A-Za-z0-9]+/g;

/** "p95 checkout" → "P95Checkout", a metric-style rule name. */
export function ruleNameFor(title: string, comparator: Comparator): string {
  const words = title.match(PASCAL_WORD) ?? [];
  const body = words.map((w) => w[0].toUpperCase() + w.slice(1)).join("");
  const suffix = comparator.startsWith("below") ? "Low" : "High";
  const name = body ? `${body}${suffix}` : `Panel${suffix}`;
  return /^[0-9]/.test(name) ? `Panel${name}` : name;
}

const DEFAULT_THRESHOLDS: Record<string, string> = { percent: "5", ms: "500", s: "1", count: "100", bytes: "1000000" };

/** The starting point of the form for a panel. */
export function defaultChoices(panel: Panel): AlertChoices {
  const series = panel.series ?? [];
  const poor = series.find((s) => s.thresholds)?.thresholds?.poor;
  return {
    name: ruleNameFor(panel.title || panel.id, "above"),
    comparator: "above",
    threshold: poor !== undefined ? String(poor) : (DEFAULT_THRESHOLDS[panel.unit ?? ""] ?? "1"),
    window: "5m",
    hold: "5m",
    severity: "warning",
    series: 0,
  };
}

/** The unit a threshold is typed in, for the form's caption. */
export function thresholdUnit(panel: Panel): string {
  switch (panel.unit) {
    case "percent":
      return "%";
    case "ms":
      return "ms";
    case "s":
      return "s";
    case "bytes":
      return "bytes";
    default:
      return "";
  }
}

/** A threshold typed in the panel's unit, as the number the SQL compares: 5 (%) is 0.05. */
function sqlThreshold(panel: Panel, typed: number): number {
  return panel.unit === "percent" ? Number((typed / 100).toPrecision(12)) : typed;
}

/** What the panel plots: the combined value, or one series. */
function plotsCombined(panel: Panel): boolean {
  return !!panel.ratio || !!panel.formula;
}

/** Whether the form should offer a choice of series. */
export function needsSeriesChoice(panel: Panel): boolean {
  return panel.kind !== "sql" && !plotsCombined(panel) && (panel.series?.length ?? 0) > 1;
}

// ── The snippet ──────────────────────────────────────────────────────────────

function ruleOf(panel: Panel, choices: AlertChoices, expr: string, scope: AlertScope, unit: string, groupLabels: string[]): DeclaredAlertRule {
  const comparator = COMPARATORS.find((c) => c.value === choices.comparator)!.label;
  const where = scope.dashboardTitle ? `${scope.dashboardTitle} › ${panel.title || panel.id}` : panel.title || panel.id;
  const first = groupLabels[0];
  return {
    alert: choices.name,
    expr,
    for: choices.hold,
    interval: "1m",
    keepFiringFor: "",
    labels: { severity: choices.severity },
    annotations: {
      summary: `${first ? `{{ $labels.${first} }}: ` : ""}${panel.title || panel.id} is {{ $value }}`,
      description: `Created from the dashboard panel "${where}": fires when the value is ${comparator} ${choices.threshold}${unit ? ` ${unit}` : ""} for ${choices.hold}, over the last ${choices.window}.`,
    },
    source: "user",
    defaultId: "",
  };
}

/** Group-by labels the query will return, for the summary template. */
function labelsOf(panel: Panel): string[] {
  const taken = new Set<string>();
  return (panel.groupBy ?? []).map((key) => {
    const label = labelName(key, taken);
    taken.add(label);
    return label;
  });
}

/**
 * The alert for a panel and the reader's choices. `unsupported` when the panel
 * is not a number to watch; otherwise the SQL and KCL, with a problem recorded
 * for every field that is not valid yet and for an expr the control plane would
 * refuse.
 */
export function buildAlertSnippet(panel: Panel, choices: AlertChoices, scope: AlertScope, declared?: DeclaredRules): AlertSnippetResult {
  const early = unsupportedReason(panel);
  if (early) return early;

  const fieldProblems: AlertSnippet["fieldProblems"] = {};
  const name = choices.name.trim();
  if (!name) fieldProblems.name = "Give the rule a name.";
  else if (!ALERT_NAME.test(name)) fieldProblems.name = "Letters, digits and _, not starting with a digit.";
  else if (name.length > MAX_NAME_LEN) fieldProblems.name = `At most ${MAX_NAME_LEN} characters.`;
  else if (declared?.all.includes(name)) fieldProblems.name = `${scope.envName} already declares a rule called ${name}. Pick another name.`;

  const typed = Number(choices.threshold);
  if (!choices.threshold.trim() || !Number.isFinite(typed)) fieldProblems.threshold = "A number, like 0.5.";
  else if (typed < 0 && ["percent", "ms", "s", "bytes", "count"].includes(panel.unit ?? "")) fieldProblems.threshold = "This panel's values are never negative.";

  const windowSeconds = durationSeconds(choices.window.trim());
  if (windowSeconds === null) fieldProblems.window = 'Like "5m" or "1h": a number and s, m or h.';
  else if (windowSeconds < MIN_WINDOW_SECONDS) fieldProblems.window = "At least 1m: rules are evaluated once a minute.";
  else if (windowSeconds > MAX_WINDOW_SECONDS) fieldProblems.window = "At most 7 days: telemetry is not kept longer.";

  if (durationSeconds(choices.hold.trim()) === null) fieldProblems.hold = 'Like "5m" or "1h30m".';
  if (!SEVERITIES.includes(choices.severity)) fieldProblems.severity = "critical, warning or info.";

  const notes: string[] = [];
  const warnings: string[] = [];
  const path = kclPath(scope.envName);
  const base = { fieldProblems, path, notes, warnings };
  if (fieldProblems.threshold || fieldProblems.window) {
    return { status: "ok", expr: null, exprProblem: null, rule: null, block: null, ready: false, ...base };
  }

  let expr: string;
  try {
    const seconds = windowSeconds as number;
    const threshold = sqlThreshold(panel, typed);
    const trimmed: AlertChoices = { ...choices, name, window: choices.window.trim(), hold: choices.hold.trim() };
    if (panel.kind === "sql") expr = sqlPanelSql(panel, trimmed, seconds, threshold, scope, notes, warnings);
    else if (panel.signal === "metrics") expr = metricsSql(panel, trimmed, seconds, threshold, scope, notes);
    else expr = builderSql(panel, trimmed, seconds, threshold, scope, notes);
  } catch (error) {
    if (error instanceof Refused) return { status: "unsupported", reason: error.message, instead: error.instead };
    throw error;
  }

  const exprProblem = alertExprProblem(expr);
  notes.push(`Scoped to ${scope.envName}: the control plane fills in ${ENV_ID_PARAM} with the id of the environment the rule is declared for.`);
  if (scope.services.length) notes.push(`Limited to ${scope.services.join(", ")}, the dashboard's service filter.`);

  const unit = thresholdUnit(panel);
  const declaredRule = ruleOf(panel, { ...choices, name, window: choices.window.trim(), hold: choices.hold.trim() }, expr, scope, unit, labelsOf(panel));
  const ready = Object.values(fieldProblems).every((v) => !v) && !exprProblem;
  return {
    status: "ok",
    expr,
    exprProblem,
    rule: alertRuleKcl(declaredRule),
    block: ruleAsCode(declaredRule, scope.envName)[0].kcl,
    ready,
    ...base,
  };
}
