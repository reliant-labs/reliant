// Copyright (c) 2025 Reliant Labs

/**
 * What a grouped panel's series ARE, before anything draws them: a stable
 * colour each, which ones are errors, the figures a legend shows (last, max,
 * total or average), and which of them a busy chart draws (the top N, the rest
 * folded into one "Other"). Pure, so the chart, the legend, the bars and the
 * pie agree on every one of these without asking each other.
 *
 * ── COLOURS ────────────────────────────────────────────────────────────────
 *
 * The categorical palette is the `--chart-1..6` tokens (charts/chartPalette.ts,
 * themes/chart-palette.css); a series that is an error takes `--destructive`
 * (the forge-ui `danger` token) instead, so red always means "error" and never
 * "the fifth series". Colours are dealt in rank order (largest first) over the
 * WHOLE result, not the drawn subset: the six series a busy chart draws get the
 * six distinct colours, and a series keeps its colour when "Show all" or
 * isolating one changes what is drawn. Past six the palette repeats; a repeat
 * is drawn dotted so two series never look identical.
 *
 * ── ERRORS ARE NEVER FOLDED AWAY ───────────────────────────────────────────
 *
 * The tail past the top N folds into "Other", but its error series fold into
 * their own "Other errors", in the danger colour: a chart of reconciles by
 * result whose error lines are small must still show red, not hide it inside a
 * grey total.
 *
 * ── WHAT ADDS UP ───────────────────────────────────────────────────────────
 *
 * "Other" is the sum of the folded series only when summing them means
 * something: counts, sums, increases and rates partition into groups, so their
 * sum is the rest of the total. A p95, an average or a ratio does not add up;
 * the folded series are then left out of the chart and named in the legend,
 * never summed into a wrong number. A legend "Total" (a sum over time) is
 * offered only for counts, sums and increases; a rate's total over buckets is
 * not a quantity, so it shows an average instead.
 */

import type { Panel, PanelResult, Series, SeriesIntent, Unit } from "@/services/controlPlane/observability/types";

import { CHART_PALETTE, DANGER_PALETTE } from "../charts/chartPalette";
import { groupLabel } from "../obsFilters";

// The palette is shared with every other chart (charts/chartPalette.ts).
export { CHART_PALETTE, DANGER_PALETTE, OTHER_TOKEN, tokenColor } from "../charts/chartPalette";

/** How many series a line draws before folding the rest into "Other": the palette's size, so no two drawn series repeat a colour. */
export const DEFAULT_TOP_N = CHART_PALETTE.length;
const MAX_TOP_N = 50;

const ERROR_WORDS = new Set([
  "error",
  "errors",
  "err",
  "failed",
  "fail",
  "failure",
  "failures",
  "fatal",
  "panic",
  "crash",
  "crashed",
  "exception",
  "critical",
  "timeout",
  "5xx",
]);

/** Whether a value names a failure: "error", "Failed", "5xx", "503", "reconcile_error". Not "errorless" or "requeue_after". */
export function isErrorWord(value: string | undefined): boolean {
  if (!value) return false;
  return value
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .some((token) => ERROR_WORDS.has(token) || /^5\d\d$/.test(token));
}

/** The declared series a result series answers: by label, or the numerator of a ratio or formula. */
export function intentOf(series: Pick<Series, "label">, panel: Pick<Panel, "series">): SeriesIntent | undefined {
  return panel.series.find((s) => s.label === series.label) ?? (series.label === "ratio" || series.label === "formula" ? panel.series[0] : undefined);
}

/**
 * A series is an error series when it counts failures by construction (its
 * declared `where` keeps only errors: span.status eq error, metric.attr.result
 * eq error), when its label says so ("errors"), or when its group IS a
 * failure ("result: error", "status: 503").
 */
export function isErrorSeries(series: Pick<Series, "label" | "group">, intent?: SeriesIntent): boolean {
  if (isErrorWord(series.label)) return true;
  if (Object.values(series.group).some(isErrorWord)) return true;
  return (intent?.where ?? []).some((p) => (p.op === "eq" || p.op === "in") && (isErrorWord(p.value) || (p.values ?? []).some(isErrorWord)));
}

export type Stat = "last" | "max" | "total" | "avg";

export interface SeriesStats {
  last: number | null;
  max: number | null;
  total: number | null;
  avg: number | null;
}

export function statsOf(values: readonly (number | null)[]): SeriesStats {
  let last: number | null = null;
  let max: number | null = null;
  let total = 0;
  let n = 0;
  for (const v of values) {
    if (v === null || !Number.isFinite(v)) continue;
    last = v;
    max = max === null ? v : Math.max(max, v);
    total += v;
    n++;
  }
  return { last, max, total: n ? total : null, avg: n ? total / n : null };
}

/** Which figures a panel's series support (see "What adds up"). */
export interface Additivity {
  /** The series partition a whole: "Other" is their sum, and stacking shows the whole. */
  foldable: boolean;
  /** A sum over time is a quantity: the legend offers Total, else Average. */
  totalable: boolean;
}

const FOLDABLE_AGGS = new Set(["count", "sum", "increase", "rate"]);
const TOTALABLE_AGGS = new Set(["count", "sum", "increase"]);

export function additivityOf(panel: Pick<Panel, "series" | "ratio" | "formula" | "kind">, unit: Unit): Additivity {
  if (panel.ratio || panel.formula || unit === "percent" || panel.kind === "sql" || panel.kind === "postgres" || panel.series.length === 0) {
    // A sql panel declares no aggregate: whether its numbers add up is unknown, so nothing is summed for it.
    return { foldable: false, totalable: false };
  }
  return {
    foldable: panel.series.every((s) => FOLDABLE_AGGS.has(s.agg)),
    totalable: panel.series.every((s) => TOTALABLE_AGGS.has(s.agg)),
  };
}

/** The legend's figures, in column order: the latest value, the peak, and a total or an average. */
export function statColumns(additivity: Additivity): Stat[] {
  return ["last", "max", additivity.totalable ? "total" : "avg"];
}

/** What a busy chart is ranked by: its share of the total when the values add up, else its latest value. */
export function rankStat(additivity: Additivity): Stat {
  return additivity.totalable ? "total" : "last";
}

/** One series as every view of it sees it. */
export interface SeriesEntry {
  /** Unique within the result: the label and the group. */
  key: string;
  label: string;
  /** A palette token (CHART_PALETTE, DANGER_PALETTE). */
  token: string;
  /** The palette came round again: draw it dotted so it differs from its namesake. */
  repeat: boolean;
  error: boolean;
  source: Series;
  values: (number | null)[];
  stats: SeriesStats;
}

export function seriesKey(series: Pick<Series, "label" | "group">): string {
  const group = Object.keys(series.group)
    .sort()
    .map((k) => `${k}=${series.group[k]}`)
    .join("\u0000");
  return `${series.label}\u0001${group}`;
}

/** "api — requests", "api" for a ratio's per-group series, or the series' own label ungrouped. */
export function seriesLabel(series: Pick<Series, "label" | "group">, panel: Pick<Panel, "kind" | "series">, keys: string[] = []): string {
  const group = Object.keys(series.group).length ? groupLabel(series.group, keys.filter((k) => k in series.group)) : "";
  if (!group) return series.label;
  // SQL may return several numeric columns for each group without declaring
  // panel.series. Keep their response labels, even when one is named "ratio".
  if (panel.kind !== "sql" && panel.kind !== "postgres" && (series.label === "ratio" || panel.series.length <= 1)) return group;
  return `${group} — ${series.label}`;
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

/**
 * Deals colours over `items` in the order given (callers pass them largest
 * first), errors from the danger palette. Returns each item's token and
 * whether it is a palette repeat.
 */
export function dealColors<T>(items: readonly T[], errorOf: (item: T) => boolean): { token: string; repeat: boolean }[] {
  const out = new Array<{ token: string; repeat: boolean }>(items.length);
  let normal = 0;
  let danger = 0;
  for (let index = 0; index < items.length; index++) {
    const error = errorOf(items[index]);
    if (error) {
      out[index] = { token: DANGER_PALETTE[danger % DANGER_PALETTE.length], repeat: danger >= DANGER_PALETTE.length };
      danger++;
    } else {
      out[index] = { token: CHART_PALETTE[normal % CHART_PALETTE.length], repeat: normal >= CHART_PALETTE.length };
      normal++;
    }
  }
  return out;
}

/** Every series of a time-series result on the shared axis, in the result's order, coloured by rank on `rank`. */
export function seriesEntries(
  panel: Panel,
  data: Pick<PanelResult, "series">,
  keys: string[],
  timestamps: number[],
  align: (timestamps: number[], s: Series) => (number | null)[],
  rank: Stat
): SeriesEntry[] {
  const base = data.series.map((source) => {
    const values = align(timestamps, source);
    return { source, label: seriesLabel(source, panel, keys), values, error: isErrorSeries(source, intentOf(source, panel)), stats: statsOf(values) };
  });
  const ranked = rankEntries(base, rank);
  const colors = dealColors(ranked, (b) => b.error);
  const colorOf = new Map(ranked.map((b, index) => [b, colors[index]]));
  return base.map((b) => ({
    key: seriesKey(b.source),
    label: b.label,
    token: colorOf.get(b)!.token,
    repeat: colorOf.get(b)!.repeat,
    error: b.error,
    source: b.source,
    values: b.values,
    stats: b.stats,
  }));
}

/** Largest first by `stat`; a series with no value last; ties in label order. */
export function rankEntries<T extends { label: string; stats: SeriesStats }>(entries: readonly T[], stat: Stat): T[] {
  return [...entries].sort((a, b) => {
    const x = a.stats[stat];
    const y = b.stats[stat];
    if (x === null && y === null) return collator.compare(a.label, b.label);
    if (x === null) return 1;
    if (y === null) return -1;
    return y - x || collator.compare(a.label, b.label);
  });
}

/** The panel's top N: its display.topN when declared, else the palette's size. */
export function topNOf(panel: Pick<Panel, "display">): number {
  const n = panel.display?.topN;
  return typeof n === "number" && Number.isInteger(n) && n >= 1 ? Math.min(n, MAX_TOP_N) : DEFAULT_TOP_N;
}

export interface Folded<T> {
  shown: T[];
  rest: T[];
}

/** The top `n` of an already-ranked list and the rest; folding a single series into "Other" hides it for nothing, so it is kept. */
export function foldTopN<T>(ranked: readonly T[], n: number): Folded<T> {
  if (ranked.length <= n + 1) return { shown: [...ranked], rest: [] };
  return { shown: ranked.slice(0, n), rest: ranked.slice(n) };
}

/** The point-by-point sum of `series`; a bucket none of them has is a gap. */
export function sumSeries(series: readonly { values: readonly (number | null)[] }[], length: number): (number | null)[] {
  const out = new Array<number | null>(length).fill(null);
  for (const s of series) {
    for (let i = 0; i < length; i++) {
      const v = s.values[i];
      if (v === null || v === undefined || !Number.isFinite(v)) continue;
      out[i] = (out[i] ?? 0) + v;
    }
  }
  return out;
}

// ── The shared time axis ────────────────────────────────────────────────────

/** Every bucket any series has — a grouped panel's series can each skip buckets they had no rows in. */
export function unionTimestamps(series: { timestamps: number[] }[]): number[] {
  const all = new Set<number>();
  series.forEach((s) => s.timestamps.forEach((t) => all.add(t)));
  return [...all].sort((a, b) => a - b);
}

/** A series on the shared x axis, a missing bucket as a gap (null). */
export function alignTo(timestamps: number[], series: { timestamps: number[]; values: (number | null)[] }): (number | null)[] {
  const byTime = new Map(series.timestamps.map((t, i) => [t, series.values[i] ?? null]));
  return timestamps.map((t) => byTime.get(t) ?? null);
}

/**
 * A comparison series moved forward by `offsetSec` onto the current axis:
 * each current bucket takes the shifted bucket nearest it (within half a
 * step), so two windows whose buckets do not start on the same second still
 * line up.
 */
export function alignShifted(timestamps: number[], series: { timestamps: number[]; values: (number | null)[] }, offsetSec: number): (number | null)[] {
  if (timestamps.length === 0) return [];
  const step = timestamps.length > 1 ? timestamps[1] - timestamps[0] : 60;
  const shifted = series.timestamps.map((t) => t + offsetSec);
  return timestamps.map((t) => {
    let best = -1;
    let distance = Infinity;
    for (let i = 0; i < shifted.length; i++) {
      const d = Math.abs(shifted[i] - t);
      if (d < distance) {
        distance = d;
        best = i;
      }
    }
    return best >= 0 && distance <= step / 2 ? (series.values[best] ?? null) : null;
  });
}
