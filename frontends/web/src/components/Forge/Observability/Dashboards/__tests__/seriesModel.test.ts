// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { Panel } from "@/services/controlPlane/observability/types";

import {
  additivityOf,
  alignTo,
  CHART_PALETTE,
  DANGER_PALETTE,
  dealColors,
  foldTopN,
  isErrorSeries,
  isErrorWord,
  rankEntries,
  seriesEntries,
  statsOf,
  sumSeries,
  topNOf,
} from "../seriesModel";

const panel = (extra: Partial<Panel> = {}): Panel => ({ id: "p", title: "P", kind: "line", signal: "traces", series: [{ label: "n", agg: "count" }], ...extra });

describe("errors", () => {
  it("recognises a failure by word, not by substring", () => {
    for (const word of ["error", "Errors", "failed", "reconcile_error", "5xx", "503", "timeout", "FATAL"]) expect(isErrorWord(word), word).toBe(true);
    for (const word of ["errorless", "success", "requeue_after", "200", "4xx", "", undefined]) expect(isErrorWord(word), String(word)).toBe(false);
  });

  it("calls a series an error by its group, its label, or a where that keeps only failures", () => {
    expect(isErrorSeries({ label: "reconciles", group: { "metric.attr.result": "error" } })).toBe(true);
    expect(isErrorSeries({ label: "errors", group: {} })).toBe(true);
    expect(isErrorSeries({ label: "failed", group: {} }, { label: "failed", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] })).toBe(true);
    // Excluding errors is the opposite.
    expect(isErrorSeries({ label: "ok", group: {} }, { label: "ok", agg: "count", where: [{ field: "span.status", op: "neq", value: "error" }] })).toBe(false);
    expect(isErrorSeries({ label: "reconciles", group: { "metric.attr.result": "requeue_after" } })).toBe(false);
  });
});

describe("colours", () => {
  it("deals the palette in the order given (largest first), errors from the danger tokens, and marks repeats", () => {
    const labels = ["a", "error", "b", ...Array.from({ length: 8 }, (_, i) => `z${i}`)];
    const colors = dealColors(labels, (l) => isErrorWord(l));
    expect(colors[0]).toEqual({ token: CHART_PALETTE[0], repeat: false });
    expect(colors[1]).toEqual({ token: DANGER_PALETTE[0], repeat: false });
    expect(colors[2]).toEqual({ token: CHART_PALETTE[1], repeat: false });
    // a, b, z0..z3 fill the six; z4 comes round again, dotted.
    expect(colors[labels.indexOf("z3")]).toEqual({ token: CHART_PALETTE[5], repeat: false });
    expect(colors[labels.indexOf("z4")]).toEqual({ token: CHART_PALETTE[0], repeat: true });
  });

  it("gives the largest series the distinct colours and keeps them whatever is drawn", () => {
    const series = ["small", "big", "mid"].map((name, i) => ({ label: "n", group: { k: name }, timestamps: [1], values: [[1, 9, 5][i]], value: null }));
    const entries = seriesEntries(panel({ groupBy: ["k"] }), { series }, ["k"], [1], alignTo, "total");
    expect(entries.map((e) => [e.label, e.token])).toEqual([
      ["small", CHART_PALETTE[2]],
      ["big", CHART_PALETTE[0]],
      ["mid", CHART_PALETTE[1]],
    ]);
  });
});

describe("what adds up", () => {
  it("folds and totals counts and sums; folds but averages rates; does neither for a p95, a ratio or a percent", () => {
    expect(additivityOf(panel(), "")).toEqual({ foldable: true, totalable: true });
    expect(additivityOf(panel({ signal: "metrics", series: [{ label: "r", agg: "rate", field: "m", metricType: "sum" }] }), "")).toEqual({ foldable: true, totalable: false });
    expect(additivityOf(panel({ series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }] }), "ms")).toEqual({ foldable: false, totalable: false });
    expect(additivityOf(panel({ ratio: true, series: [{ label: "a", agg: "count" }, { label: "b", agg: "count" }] }), "")).toEqual({ foldable: false, totalable: false });
    expect(additivityOf(panel(), "percent")).toEqual({ foldable: false, totalable: false });
    expect(additivityOf(panel({ kind: "sql", series: [] }), "")).toEqual({ foldable: false, totalable: false });
  });

  it("states last, max, total and average, skipping gaps", () => {
    expect(statsOf([1, null, 5, 2])).toEqual({ last: 2, max: 5, total: 8, avg: 8 / 3 });
    expect(statsOf([null, null])).toEqual({ last: null, max: null, total: null, avg: null });
  });

  it("sums series point by point, a bucket none has stays a gap", () => {
    expect(sumSeries([{ values: [1, null, 2] }, { values: [3, null, null] }], 3)).toEqual([4, null, 2]);
  });
});

describe("top N", () => {
  const entry = (label: string, total: number | null) => ({ label, stats: { last: total, max: total, total, avg: total } });

  it("ranks largest first, no value last, ties by label", () => {
    expect(rankEntries([entry("b", 1), entry("a", 1), entry("n", null), entry("c", 9)], "total").map((e) => e.label)).toEqual(["c", "a", "b", "n"]);
  });

  it("folds the tail, but never a single series (hiding one behind 'Other' saves nothing)", () => {
    expect(foldTopN([1, 2, 3, 4, 5], 3)).toEqual({ shown: [1, 2, 3], rest: [4, 5] });
    expect(foldTopN([1, 2, 3, 4], 3)).toEqual({ shown: [1, 2, 3, 4], rest: [] });
  });

  it("takes the panel's topN, else the palette's size", () => {
    expect(topNOf(panel())).toBe(CHART_PALETTE.length);
    expect(topNOf(panel({ display: { topN: 3 } }))).toBe(3);
    expect(topNOf(panel({ display: { topN: 0 } }))).toBe(CHART_PALETTE.length);
    expect(topNOf(panel({ display: { topN: 500 } }))).toBe(50);
  });
});
