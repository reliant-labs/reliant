// Copyright (c) 2025 Reliant Labs

/**
 * What a metric's kind allows, and what a freshly picked metric defaults to:
 * DESIGN §3.2's aggregation table on the console side. The server refuses what
 * a kind cannot do (InvalidArgument); this is what the builder offers, so a
 * reader is never shown a choice that can only fail.
 */

import type { MetricCatalogEntry, MetricKind } from "@/services/controlPlane/observability/metricsTypes";
import type { SeriesIntent } from "@/services/controlPlane/observability/types";

export { catalogGroup } from "@/services/controlPlane/observability/metricsTypes";

/** A histogram's default quantile: a percentile, because an average latency hides the tail. */
export const DEFAULT_QUANTILE = 0.95;

const GAUGE_AGGS = ["avg", "min", "max", "last", "sum"];
const COUNTER_AGGS = ["rate", "increase", "avg", "min", "max", "last"];
const UP_DOWN_AGGS = ["sum", "avg", "min", "max", "last"];
const HISTOGRAM_AGGS = ["quantile", "avg", "min", "max", "sum", "rate", "increase", "count"];
// A summary's stored quantiles cannot be merged across series, so `quantile` is not offered;
// avg, count and rate run over its Sum and Count.
const SUMMARY_AGGS = ["avg", "count", "rate"];

/**
 * The aggregations a metric can be drawn with, in the order the builder lists
 * them (the default first). A monotonic sum's `sum` is its `increase`, so it is
 * not offered twice; an up-down counter has no rate; only a histogram has a
 * quantile.
 */
export function allowedAggs(entry: Pick<MetricCatalogEntry, "kind" | "monotonic">): string[] {
  switch (entry.kind) {
    case "gauge":
      return [...GAUGE_AGGS];
    case "sum":
      return entry.monotonic ? [...COUNTER_AGGS] : [...UP_DOWN_AGGS];
    case "histogram":
    case "exp_histogram":
      return [...HISTOGRAM_AGGS];
    case "summary":
      return [...SUMMARY_AGGS];
    default:
      return [];
  }
}

/** The kind as a series' `metricType`: the vocabulary dashspec reads (gauge, sum, histogram, exp_histogram, summary). */
export function metricTypeOf(kind: MetricKind): string | undefined {
  return kind === "unknown" ? undefined : kind;
}

/** The aggregation a metric is drawn with when none is chosen. */
export function defaultAgg(entry: Pick<MetricCatalogEntry, "kind" | "monotonic">): { agg: string; quantile?: number } {
  switch (entry.kind) {
    case "sum":
      return { agg: entry.monotonic ? "rate" : "sum" };
    case "histogram":
    case "exp_histogram":
      return { agg: "quantile", quantile: DEFAULT_QUANTILE };
    default:
      return { agg: "avg" };
  }
}

/** The series picking `entry` adds: the metric, its kind, and the type-correct default aggregation. */
export function defaultSeriesFor(entry: MetricCatalogEntry, label: string): SeriesIntent {
  const { agg, quantile } = defaultAgg(entry);
  const series: SeriesIntent = { label, agg, field: entry.name };
  const type = metricTypeOf(entry.kind);
  if (type) series.metricType = type;
  if (quantile !== undefined) series.quantile = quantile;
  return series;
}

/** The label of the n-th series: A…J, forge's formula references. */
export function seriesLabel(index: number): string {
  return String.fromCharCode(65 + index);
}
