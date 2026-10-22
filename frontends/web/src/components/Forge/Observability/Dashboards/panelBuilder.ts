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

import type { Dashboard, Panel, Predicate, SeriesIntent, Unit } from "@/services/controlPlane/observability/types";

import { newPanelId } from "./dashboardModel";

export const VISUALIZATIONS = [
  { kind: "line", label: "Time series" },
  { kind: "number", label: "Stat" },
  { kind: "table", label: "Table" },
  { kind: "funnel", label: "Funnel" },
  { kind: "issues", label: "Issues" },
  { kind: "sessions", label: "Sessions" },
  { kind: "search", label: "Rows" },
] as const;

export type Visualization = (typeof VISUALIZATIONS)[number]["kind"];

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
  const base: Panel = { id: panel.id, title: panel.title, description: panel.description, kind, signal: panel.signal || "traces", series: [], width: panel.width, height: panel.height, where: panel.where };
  switch (kind) {
    case "funnel":
      return { ...base, signal: "traces", funnel: panel.funnel ?? { steps: ["", ""], window: "1h", by: "session" }, where: browserWhere(panel.where) };
    case "issues":
      return { ...base, signal: "traces", where: browserWhere(panel.where) };
    case "sessions":
      return { ...base, signal: "traces", hasError: panel.hasError, where: browserWhere(panel.where) };
    case "search":
      return { ...base, signal: panel.signal === "logs" ? "logs" : "traces", columns: panel.columns?.length ? panel.columns : defaultColumns(panel.signal) };
    default: {
      const series = panel.series.length ? panel.series : [{ label: "requests", agg: "count" }];
      return {
        ...base,
        signal: panel.signal === "logs" || panel.signal === "metrics" ? panel.signal : "traces",
        series,
        groupBy: panel.groupBy,
        ratio: kind === "line" && panel.ratio && series.length === 2 ? true : undefined,
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
export function withSignal(panel: Panel, signal: string): Panel {
  if (signal === panel.signal) return panel;
  const series = panel.series.map((s): SeriesIntent => {
    const cleared = { ...s, metricType: signal === "metrics" ? (s.metricType ?? "gauge") : undefined, where: (s.where ?? []).filter((p) => !isErrorPredicate(p)) };
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
