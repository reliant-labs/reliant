// Copyright (c) 2025 Reliant Labs

/**
 * What the explorer derives from a metrics QueryPanel answer for display: the
 * unit to format values in, a series split into its complete and still-open
 * buckets (the open ones drawn dashed), the legend's figures, the attribute
 * vocabulary of the builder, and the words for the server's notes. Pure, so
 * each rule is tested on its own.
 */

import type { MetricAttribute, MetricCatalogEntry, SeriesMeta } from "@/services/controlPlane/observability/metricsTypes";
import type { PanelResult, Series, Unit } from "@/services/controlPlane/observability/types";
import { Code, ConnectError } from "@connectrpc/connect";

import type { ValueUnit } from "../format";

// ── Units ────────────────────────────────────────────────────────────────────

/**
 * A UCUM unit (what OTel metrics carry, and SeriesMeta.valueUnit) as the
 * console's formatter unit: `s` and `ms` are durations, `By` bytes, anything
 * per second a rate, and every other unit (`1`, `{request}`) a plain count.
 */
export function valueUnitOf(ucum: string): ValueUnit {
  const unit = ucum.trim();
  if (unit.endsWith("/s")) return "rate";
  switch (unit) {
    case "s":
      return "s";
    case "ms":
      return "ms";
    case "By":
      return "bytes";
    default:
      return "count";
  }
}

/** The one unit a chart's axis can use: the series' common unit, or a plain count when they disagree. */
export function chartUnitOf(meta: readonly SeriesMeta[]): ValueUnit {
  const units = new Set(meta.filter((m) => m.metric || m.valueUnit).map((m) => valueUnitOf(m.valueUnit)));
  return units.size === 1 ? [...units][0]! : "count";
}

/** The ValueUnit as a panel Unit (for PanelBody's number and table): a rate has no panel unit. */
export function panelUnitOf(unit: ValueUnit): Unit {
  return unit === "rate" || unit === "cores" ? "" : unit;
}

/** "{order}" → "orders", "By" → "bytes": a unit as a reader says it, for the detail pane. */
export function unitLabel(ucum: string): string {
  const unit = ucum.trim();
  if (!unit) return "no unit";
  if (unit === "1") return "ratio";
  if (unit === "By") return "bytes";
  if (unit === "s") return "seconds";
  if (unit === "ms") return "milliseconds";
  if (unit === "us") return "microseconds";
  const braced = /^\{(.+)\}$/.exec(unit);
  return braced ? braced[1]! : unit;
}

// ── Series ───────────────────────────────────────────────────────────────────

/**
 * A series as two lines on the same axis: `complete` holds the closed buckets
 * and `open` the ones still filling (Series.partial), joined at the last closed
 * point so the dashed tail continues the line rather than floating beside it.
 * Without `partial`, `open` is all gaps.
 */
export function splitPartial(values: readonly (number | null)[], partial: readonly boolean[] | undefined): { complete: (number | null)[]; open: (number | null)[] } {
  const isOpen = (i: number) => !!partial?.[i];
  const complete = values.map((v, i) => (isOpen(i) ? null : v));
  const open = values.map((v, i) => (isOpen(i) ? v : null));
  const firstOpen = values.findIndex((_, i) => isOpen(i));
  if (firstOpen > 0 && !isOpen(firstOpen - 1)) open[firstOpen - 1] = values[firstOpen - 1] ?? null;
  return { complete, open };
}

/** Aggregations whose values add up across series: a stack of them is their total. */
const ADDITIVE_AGGS = new Set(["rate", "increase", "sum", "count"]);

/**
 * How an area chart fills: stacked when every drawn series is an additive
 * aggregation of a metric (rate, increase, sum, count) in one unit — the top
 * edge is then a real total — and a plain translucent fill otherwise (a
 * percentile, an average or a formula does not add, and stacking it would draw a
 * number nothing measured).
 */
export function areaModeFor(meta: readonly SeriesMeta[], labels: readonly string[]): "stacked" | "fill" {
  if (labels.length < 2) return "fill";
  const found = labels.map((label) => meta.find((m) => m.label === label));
  const additive = found.every((m) => !!m && m.metric !== "" && ADDITIVE_AGGS.has(m.agg));
  const units = new Set(found.map((m) => m?.valueUnit));
  return additive && units.size === 1 ? "stacked" : "fill";
}

export interface SeriesFigures {
  /** The newest complete bucket's value; an open bucket is not a reading yet. */
  last: number | null;
  avg: number | null;
  max: number | null;
}

/** The legend's figures over the complete buckets: an open one would drag the last and the average down. */
export function seriesFigures(values: readonly (number | null)[], partial?: readonly boolean[]): SeriesFigures {
  const present = values.filter((v, i): v is number => v !== null && Number.isFinite(v) && !partial?.[i]);
  if (present.length === 0) return { last: null, avg: null, max: null };
  return {
    last: present[present.length - 1]!,
    avg: present.reduce((a, b) => a + b, 0) / present.length,
    max: Math.max(...present),
  };
}

const groupKeyOf = (group: Record<string, string>) =>
  Object.keys(group)
    .sort()
    .map((k) => `${k}=${group[k]}`)
    .join("\u0000");

/** How many distinct groups a result draws (each group is one series per query). */
export function shownGroups(result: Pick<PanelResult, "series" | "rows">): number {
  const keys = new Set<string>();
  result.series.forEach((s) => keys.add(groupKeyOf(s.group)));
  result.rows.forEach((r) => keys.add(groupKeyOf(r.group)));
  return keys.size;
}

/** "Showing 10 of 1,204 series" when the server's cap cut the result; null when everything is shown. */
export function capNote(result: Pick<PanelResult, "series" | "rows" | "seriesTotal" | "truncated">): { shown: number; total: number } | null {
  const shown = shownGroups(result);
  if (result.seriesTotal > shown) return { shown, total: result.seriesTotal };
  if (result.truncated && shown > 0) return { shown, total: 0 };
  return null;
}

/** The series of one group value of `field`, in the order the result lists them: a breakdown's small multiples. */
export function seriesByValue(series: readonly Series[], field: string): { value: string; series: Series[] }[] {
  const out: { value: string; series: Series[] }[] = [];
  for (const s of series) {
    const value = s.group[field] ?? "";
    const bucket = out.find((entry) => entry.value === value);
    if (bucket) bucket.series.push(s);
    else out.push({ value, series: [s] });
  }
  return out;
}

// ── Attributes ───────────────────────────────────────────────────────────────

const SEMANTIC_KEYS = new Set(["service.name", "deployment.environment.name"]);

/**
 * An attribute of the metric as the dashspec field a where or group-by names:
 * the semantic `service.name` / `deployment.environment.name`, a resource
 * attribute as `resource.attr.<key>`, a data-point attribute as
 * `metric.attr.<key>` (forge dashspec field.go).
 */
export function attributeField(attribute: Pick<MetricAttribute, "key" | "resource">): string {
  if (SEMANTIC_KEYS.has(attribute.key)) return attribute.key;
  return `${attribute.resource ? "resource" : "metric"}.attr.${attribute.key}`;
}

/** A field as the reader knows it: the attribute key without the dashspec prefix. */
export function fieldLabel(field: string): string {
  return field.replace(/^(metric|resource|span|log)\.attr\./, "");
}

/** The metric's attributes, the suggested breakdowns first in the server's order, then the rest by name. */
export function orderedAttributes(attributes: readonly MetricAttribute[], suggested: readonly string[]): MetricAttribute[] {
  const rank = (a: MetricAttribute) => {
    const i = suggested.indexOf(a.key);
    return i < 0 ? suggested.length : i;
  };
  return [...attributes].sort((a, b) => rank(a) - rank(b) || a.key.localeCompare(b.key));
}

// ── Catalog ──────────────────────────────────────────────────────────────────

export const GROUP_ORDER: MetricCatalogEntry["group"][] = ["declared", "runtime", "infra", "other"];

export const GROUP_LABELS: Record<MetricCatalogEntry["group"], string> = {
  declared: "Declared",
  runtime: "Runtime",
  infra: "Infra",
  other: "Other",
};

/** What a metric's kind is called, the way a reader thinks of it. */
export function kindLabel(entry: Pick<MetricCatalogEntry, "kind" | "monotonic">): string {
  switch (entry.kind) {
    case "sum":
      return entry.monotonic ? "counter" : "up-down counter";
    case "exp_histogram":
      return "exp. histogram";
    case "unknown":
      return "unknown kind";
    default:
      return entry.kind;
  }
}

/** The catalog, filtered by a name needle and split into its groups, each by name. */
export function groupCatalog(metrics: readonly MetricCatalogEntry[], needle: string): { group: MetricCatalogEntry["group"]; metrics: MetricCatalogEntry[] }[] {
  const q = needle.trim().toLowerCase();
  const matching = metrics.filter((m) => !q || m.name.toLowerCase().includes(q) || m.description.toLowerCase().includes(q));
  return GROUP_ORDER.map((group) => ({
    group,
    metrics: matching.filter((m) => m.group === group).sort((a, b) => a.name.localeCompare(b.name)),
  })).filter((entry) => entry.metrics.length > 0);
}

// ── Notes ────────────────────────────────────────────────────────────────────

const WARNING_COPY: Record<string, string> = {
  mixed_bounds: "Histogram buckets differ across series; merged onto their union",
  mixed_temporality: "Delta and cumulative points mixed",
  step_raised: "Step raised to the export interval",
  metric_stored_as_n_kinds: "Stored as more than one kind",
};

/** A warning code from the server as a short sentence; an unknown code is shown as it came. */
export function warningText(code: string): string {
  return WARNING_COPY[code] ?? code.replace(/_/g, " ");
}

/** NotFound from GetMetric: the metric has no data in the window, a state to show rather than a failure. */
export function isNoDataInWindow(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.NotFound;
}
