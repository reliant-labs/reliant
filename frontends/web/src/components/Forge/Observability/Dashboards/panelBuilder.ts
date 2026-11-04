// Copyright (c) 2025 Reliant Labs

/**
 * The panel builder's model: a forge panel edited in place, and the moves
 * the builder offers on it — pick a visualization, an aggregation preset
 * (count, p50 / p95 / p99, avg…), errors only, a ratio, filters, group-by —
 * each one a function from panel to panel, so what the builder does is
 * testable without a drawer.
 *
 * The builder speaks only forge's spec (`forge.dashboards/v1`): what it
 * cannot express is not offered. QueryPanel compiles the spec server-side;
 * the console never writes a query.
 */

import type { Dashboard, Panel, PanelDisplay, Predicate, SeriesIntent, Unit } from "@/services/controlPlane/observability/types";

import { DISPLAY_STYLES, displayOptions, fieldOnSignal, MAX_TOP_N } from "../obsdashSpec";
import { newPanelId } from "./dashboardModel";

/**
 * Every kind forge's spec has, as the builder offers it: what it draws, in a
 * line, and where it reads from. The order is the picker's: the charts over
 * series, then the hand-written queries, then prose, then the kinds the
 * console draws from its own services.
 */
export const VISUALIZATIONS = [
  { kind: "line", label: "Time series", description: "Values over time: one line, area or bar series per series or group." },
  { kind: "number", label: "Stat", description: "One headline figure for the range, or a gauge between a min and a max." },
  { kind: "bar", label: "Bar", description: "The top groups side by side, or as the slices of a pie or donut." },
  { kind: "table", label: "Table", description: "One row per group, one column per series." },
  { kind: "heatmap", label: "Heatmap", description: "How one numeric value, such as latency, is spread over time." },
  { kind: "search", label: "Rows", description: "The matching spans or log lines themselves, as a way into search." },
  { kind: "sql", label: "ClickHouse SQL", description: "One read-only SELECT over your telemetry tables, drawn from its rows." },
  { kind: "postgres", label: "Postgres", description: "A live read-only query of one of your environment's databases." },
  { kind: "text", label: "Text", description: "Markdown with no query: a heading, a note, a runbook link." },
  { kind: "funnel", label: "Funnel", description: "How many journeys reach each product event, in order." },
  { kind: "issues", label: "Issues", description: "Errors grouped by stack, new and regressed first." },
  { kind: "sessions", label: "Sessions", description: "Browser sessions, newest first, with their replays." },
] as const;

export type Visualization = (typeof VISUALIZATIONS)[number]["kind"];

/** The kinds whose query is a statement the author writes, not series the builder composes. */
export const STATEMENT_KINDS: readonly string[] = ["sql", "postgres"];

// ── Display: one query, several pictures (forge dashspec Display) ────────────

export { DISPLAY_STYLES, displayOptions, MAX_TOP_N };

export const STYLE_LABELS: Record<string, string> = {
  line: "Line",
  area: "Area",
  bars: "Bars over time",
  bar: "Bars",
  pie: "Pie",
  donut: "Donut",
  stat: "Stat",
  gauge: "Gauge",
  funnel: "Funnel",
};

/** The style `panel` is drawn in: its own when its kind takes it, else the kind's default (empty for a kind drawn one way). */
export function styleOf(panel: Pick<Panel, "kind" | "display">): string {
  const styles = DISPLAY_STYLES[panel.kind] ?? [];
  const style = panel.display?.style;
  return style && styles.includes(style) ? style : (styles[0] ?? "");
}

/**
 * `panel` with `patch` applied to how it is drawn, normalized: the default
 * style is left unsaid, an option the style does not use is dropped (forge
 * refuses one a renderer would ignore), and a display with nothing left is
 * removed. It never touches the query.
 */
export function withDisplay(panel: Panel, patch: Partial<PanelDisplay>): Panel {
  return { ...panel, display: normalizedDisplay(panel.kind, { ...panel.display, ...patch }) };
}

function normalizedDisplay(kind: string, display: PanelDisplay | undefined): PanelDisplay | undefined {
  const styles = DISPLAY_STYLES[kind];
  if (!styles || !display) return undefined;
  const style = display.style && styles.includes(display.style) ? display.style : styles[0];
  const options = displayOptions(kind, style);
  const out: PanelDisplay = {};
  if (style !== styles[0]) out.style = style;
  if (options.stack && display.stack) out.stack = true;
  if (options.horizontal && display.horizontal) out.horizontal = true;
  if (options.gauge) {
    if (Number.isFinite(display.min)) out.min = display.min;
    if (Number.isFinite(display.max)) out.max = display.max;
  }
  if (options.topN && display.topN !== undefined && Number.isInteger(display.topN) && display.topN >= 1 && display.topN <= MAX_TOP_N) out.topN = display.topN;
  return Object.keys(out).length ? out : undefined;
}

// ── Statements and prose: what a new sql, postgres or text panel starts as ───

/** A ClickHouse statement over the scope's own parameters: requests over time, as the builder's starter panel draws them. */
export const STARTER_SQL = [
  "SELECT toStartOfInterval(Timestamp, toIntervalSecond({step:UInt32})) AS t,",
  "       count() AS requests",
  "FROM otel_traces",
  "WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}",
  "  AND SpanKind IN ('Server', 'SPAN_KIND_SERVER')",
  "  AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id']))",
  "  AND (empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))",
  "GROUP BY t",
  "ORDER BY t",
].join("\n");

/** A Postgres statement every database answers (the catalog is readable by any role), until the author picks a table. */
export const STARTER_POSTGRES_SQL = ["SELECT count(*) AS tables", "FROM information_schema.tables", "WHERE table_schema = 'public'"].join("\n");

export const STARTER_MARKDOWN = "## About this dashboard\n\nWhat it is for, and where to look when it turns red.";

/** The ways a postgres panel's result is drawn (dashspec PostgresDraw*). */
export const POSTGRES_DRAWS = ["auto", "line", "number", "table", "bar"] as const;

/** The aggregation presets of the builder: the agg, its quantile, and the field it needs. */
export const AGG_PRESETS = ["count", "p50", "p95", "p99", "avg", "max", "sum", "count_distinct"] as const;
export type AggPreset = (typeof AGG_PRESETS)[number];

export const AGG_PRESET_LABELS: Record<AggPreset, string> = {
  count: "count",
  p50: "p50",
  p95: "p95",
  p99: "p99",
  avg: "avg",
  max: "max",
  sum: "sum",
  count_distinct: "distinct",
};

const QUANTILES: Partial<Record<AggPreset, number>> = { p50: 0.5, p95: 0.95, p99: 0.99 };

/** The field a preset reads when none was named: span duration for traces. */
function defaultField(signal: string): string {
  return signal === "traces" ? "span.duration" : "";
}

/** What a series is, as a preset (a quantile other than p50/p95/p99 reads as its own number). */
export function presetOf(series: SeriesIntent): AggPreset | null {
  if (series.agg === "quantile") {
    const found = (Object.entries(QUANTILES) as [AggPreset, number][]).find(([, q]) => q === series.quantile);
    return found ? found[0] : null;
  }
  return (AGG_PRESETS as readonly string[]).includes(series.agg) ? (series.agg as AggPreset) : null;
}

/** A series set to a preset: count needs no field; the rest keep theirs, or read the signal's default. */
export function withPreset(series: SeriesIntent, preset: AggPreset, signal: string): SeriesIntent {
  const quantile = QUANTILES[preset];
  const { quantile: _old, ...rest } = series;
  if (preset === "count") return { ...rest, agg: "count", field: undefined, label: relabel(series.label, preset) };
  return {
    ...rest,
    agg: quantile ? "quantile" : preset,
    ...(quantile ? { quantile } : {}),
    field: series.field || defaultField(signal),
    label: relabel(series.label, preset),
  };
}

/** A label that was a preset's name follows the preset; one the author wrote is kept. */
function relabel(label: string, preset: AggPreset): string {
  const auto = ["", "requests", ...AGG_PRESETS, "p50", "p95", "p99", "distinct"];
  return auto.includes(label) ? (preset === "count" ? "requests" : AGG_PRESET_LABELS[preset]) : label;
}

const ERROR_PREDICATE = (signal: string): Predicate =>
  signal === "logs" ? { field: "log.severity", op: "eq", value: "error" } : { field: "span.status", op: "eq", value: "error" };

const isErrorPredicate = (p: Predicate) => (p.field === "span.status" || p.field === "log.severity") && p.op === "eq" && (p.value ?? "").toLowerCase() === "error";

export function countsOnlyErrors(series: SeriesIntent): boolean {
  return (series.where ?? []).some(isErrorPredicate);
}

/** A series narrowed to failures (failed spans, error logs), or widened back. */
export function withErrorsOnly(series: SeriesIntent, on: boolean, signal: string): SeriesIntent {
  const others = (series.where ?? []).filter((p) => !isErrorPredicate(p));
  const where = on ? [...others, ERROR_PREDICATE(signal)] : others;
  return { ...series, where: where.length ? where : undefined };
}

/** The next series' label: A, B, C… unless the author named them. */
function nextLabel(panel: Panel): string {
  const used = new Set(panel.series.map((s) => s.label));
  for (const letter of "ABCDEFGHIJ") if (!used.has(letter.toLowerCase())) return letter.toLowerCase();
  return `s${panel.series.length + 1}`;
}

export function addSeries(panel: Panel): Panel {
  return { ...panel, series: [...panel.series, { label: nextLabel(panel), agg: "count" }] };
}

export function removeSeries(panel: Panel, index: number): Panel {
  const series = panel.series.filter((_, i) => i !== index);
  return { ...panel, series, ratio: panel.ratio && series.length === 2 ? panel.ratio : undefined };
}

export function updateSeries(panel: Panel, index: number, series: SeriesIntent): Panel {
  return { ...panel, series: panel.series.map((s, i) => (i === index ? series : s)) };
}

/**
 * A ratio plots A / B (an error rate: failed requests over all requests). It
 * needs exactly two series on a line panel; turning it on makes two (B as
 * the count of everything) and the unit a percentage, which forge formats
 * from a fraction — so A / B × 100 is what the reader sees.
 */
export function withRatio(panel: Panel, on: boolean): Panel {
  if (!on) return { ...panel, ratio: undefined, unit: panel.unit === "percent" ? undefined : panel.unit };
  const first = panel.series[0] ?? { label: "errors", agg: "count" };
  const errors = countsOnlyErrors(first) ? first : withErrorsOnly({ ...first, label: first.label === "requests" ? "errors" : first.label }, true, panel.signal);
  return { ...panel, kind: "line", ratio: true, unit: "percent", series: [errors, { label: "requests", agg: "count" }] };
}

/**
 * The panel switched to another visualization: the fields the new kind
 * takes are kept or given defaults, and the ones it refuses are dropped, so
 * the switch never leaves a panel forge would reject for leftovers.
 */
export function withKind(panel: Panel, kind: Visualization): Panel {
  const layout = { id: panel.id, section: panel.section, title: panel.title, description: panel.description, width: panel.width, height: panel.height };
  const base: Panel = { ...layout, kind, signal: panel.signal || "traces", series: [], where: panel.where };
  // A statement or prose panel reads no signal and takes no filters: they are its statement's, or nobody's.
  const bare: Panel = { ...layout, kind, signal: "", series: [] };
  const display = normalizedDisplay(kind, panel.display);
  switch (kind) {
    case "sql":
      return { ...bare, sql: panel.sql ?? STARTER_SQL, minStep: panel.minStep, display, unit: panel.unit };
    case "postgres":
      return { ...bare, postgres: panel.postgres ?? { database: "", sql: STARTER_POSTGRES_SQL }, unit: panel.unit };
    case "text":
      return { ...bare, markdown: panel.markdown ?? STARTER_MARKDOWN };
    case "heatmap": {
      const signal = panel.signal === "logs" ? "logs" : "traces";
      const field = panel.series.find((s) => s.field && s.agg !== "count")?.field ?? defaultField(signal);
      return { ...base, signal, series: [{ label: "events", agg: "count", field }], unit: signal === "traces" ? "ms" : panel.unit };
    }
    case "funnel":
      return { ...base, signal: "traces", funnel: panel.funnel ?? { steps: ["", ""], window: "1h", by: "session" }, where: browserWhere(panel.where) };
    case "issues":
      return { ...base, signal: "traces", where: browserWhere(panel.where) };
    case "sessions":
      return { ...base, signal: "traces", hasError: panel.hasError, where: browserWhere(panel.where) };
    case "search":
      return { ...base, signal: panel.signal === "logs" ? "logs" : "traces", columns: panel.columns?.length ? panel.columns : defaultColumns(panel.signal) };
    default: {
      // A heatmap's one series counts events by a field: as a series to aggregate, it is a count.
      const kept = panel.kind === "heatmap" ? [] : panel.series;
      const series = kept.length ? kept : [{ label: "requests", agg: "count" }];
      const groupBy = kind === "bar" && !panel.groupBy?.length ? ["service.name"] : panel.groupBy;
      return {
        ...base,
        signal: panel.signal === "logs" || panel.signal === "metrics" ? panel.signal : "traces",
        series,
        groupBy,
        ratio: kind === "line" && panel.ratio && series.length === 2 ? true : undefined,
        formula: (kind === "line" || kind === "number") && !panel.ratio ? panel.formula : undefined,
        limit: kind === "table" || kind === "bar" ? panel.limit : undefined,
        display,
        unit: panel.unit,
      };
    }
  }
}

/** The fields the browser kinds accept in `where` (C7): the rest are dropped on a switch. */
const BROWSER_FIELDS = ["service.name", "service.version", "reliant.env.id", "deployment.environment.name", "resource.attr.rum.sessionId"];

function browserWhere(where: Predicate[] | undefined): Predicate[] | undefined {
  const kept = (where ?? []).filter((p) => BROWSER_FIELDS.includes(p.field) || p.field.startsWith("span.attr."));
  return kept.length ? kept : undefined;
}

function defaultColumns(signal: string): string[] {
  return signal === "logs" ? ["timestamp", "service.name", "log.severity", "log.body"] : ["timestamp", "service.name", "span.name", "span.duration"];
}

/** The signal switched: series fields that belong to the old signal are cleared. */
export function withSignal(next: Panel, signal: string): Panel {
  if (signal === next.signal) return next;
  // Filters and group-by fields of the old signal's own (span.kind on logs) mean nothing on the new one, and forge refuses them.
  const fits = (field: string) => !field || fieldOnSignal(field, signal);
  const where = (next.where ?? []).filter((p) => fits(p.field));
  const groupBy = (next.groupBy ?? []).filter(fits);
  const panel: Panel = { ...next, where: where.length ? where : undefined, groupBy: groupBy.length ? groupBy : undefined };
  if (panel.kind === "heatmap") {
    // A heatmap's one series counts events by a numeric field of the signal: the old signal's field means nothing on the new one.
    const label = panel.series[0]?.label || "events";
    return { ...panel, signal, series: [{ label, agg: "count", field: defaultField(signal) }], unit: signal === "traces" ? "ms" : undefined };
  }
  const series = panel.series.map((s): SeriesIntent => {
    const kept = (s.where ?? []).filter((p) => !isErrorPredicate(p) && fits(p.field));
    const cleared = { ...s, metricType: signal === "metrics" ? (s.metricType ?? "gauge") : undefined, where: kept.length ? kept : undefined };
    if (signal === "metrics") return { ...cleared, agg: s.agg === "count" || s.agg === "count_distinct" ? "avg" : s.agg, field: "" };
    if (s.agg === "last") return { ...cleared, agg: "max", field: defaultField(signal) };
    return { ...cleared, field: s.agg === "count" ? undefined : defaultField(signal) };
  });
  return { ...panel, signal, series, columns: panel.kind === "search" ? defaultColumns(signal) : panel.columns, ratio: signal === "metrics" ? undefined : panel.ratio };
}

/** A unit for what the panel measures, when the author has not picked one. */
export function suggestedUnit(panel: Panel): Unit {
  if (panel.ratio) return "percent";
  const first = panel.series[0];
  if (!first) return "";
  if (first.field === "span.duration" && first.agg !== "count" && first.agg !== "count_distinct") return "ms";
  if (first.agg === "count" || first.agg === "count_distinct") return "count";
  return "";
}

/**
 * `after` with its unit following what it now measures — when the unit was
 * the one suggested for `before` (the author never picked one by hand), it
 * becomes the one suggested for `after`: switching count to p95 makes it
 * milliseconds.
 */
export function followUnit(before: Panel, after: Panel): Panel {
  if (before.unit && before.unit !== suggestedUnit(before)) return after;
  const unit = suggestedUnit(after);
  return { ...after, unit: unit || undefined };
}

/** A new panel for `dashboard`: requests over time, across the project's server spans. */
export function starterPanel(dashboard: Dashboard): Panel {
  return {
    id: newPanelId(dashboard),
    title: "New panel",
    kind: "line",
    signal: "traces",
    where: [{ field: "span.kind", op: "eq", value: "server" }],
    series: [{ label: "requests", agg: "count" }],
    unit: "count",
    width: 12,
    height: 7,
  };
}

/** The panel with an empty filter row appended / one updated / one removed. */
export function addFilter(panel: Panel): Panel {
  return { ...panel, where: [...(panel.where ?? []), { field: "", op: "eq", value: "" }] };
}

export function updateFilter(panel: Panel, index: number, predicate: Predicate): Panel {
  return { ...panel, where: (panel.where ?? []).map((p, i) => (i === index ? predicate : p)) };
}

export function removeFilter(panel: Panel, index: number): Panel {
  const where = (panel.where ?? []).filter((_, i) => i !== index);
  return { ...panel, where: where.length ? where : undefined };
}

/** A copy of `panel` under a new id, titled as a copy, placed after it. */
export function duplicatePanel(dashboard: Dashboard, panelId: string): Dashboard {
  const index = dashboard.panels.findIndex((p) => p.id === panelId);
  if (index < 0) return dashboard;
  const source = dashboard.panels[index];
  const copy = { ...source, id: newPanelId(dashboard, source.id.replace(/-\d+$/, "")), title: `${source.title} (copy)` };
  const panels = [...dashboard.panels];
  panels.splice(index + 1, 0, copy);
  return { ...dashboard, panels };
}

/** The dashboard with `panel` replacing the one of its id, or appended when it is new. */
export function upsertPanel(dashboard: Dashboard, panel: Panel): Dashboard {
  const exists = dashboard.panels.some((p) => p.id === panel.id);
  return { ...dashboard, panels: exists ? dashboard.panels.map((p) => (p.id === panel.id ? panel : p)) : [...dashboard.panels, panel] };
}

/** The panel as the spec tab shows it: forge's JSON, two-space indented. */
export function panelJson(panel: Panel): string {
  return JSON.stringify(panel, null, 2);
}

/** A panel typed into the spec tab, or the reason it is not one. */
export function parsePanelJson(text: string): { panel: Panel } | { problem: string } {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (error) {
    return { problem: `Not JSON: ${(error as Error).message}` };
  }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return { problem: "A panel is one JSON object." };
  const doc = raw as Record<string, unknown>;
  if (typeof doc.id !== "string" || typeof doc.kind !== "string") return { problem: 'A panel needs an "id" and a "kind".' };
  return { panel: { ...(doc as unknown as Panel), series: Array.isArray(doc.series) ? (doc.series as SeriesIntent[]) : [] } };
}
