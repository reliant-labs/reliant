// Copyright (c) 2026 Reliant Labs

/**
 * The pure half of a chart's hover read-out: the nearest sample, where the
 * keys move the highlight, what the tooltip and the live region say, and
 * where the tooltip goes (flipping at the viewport's right edge).
 */

import { describe, expect, it } from "vitest";

import { describeSample, formatSampleTime, indexAtFraction, nearestIndex, placeTooltip, sampleRows, slotAnchor, slotIndexAt, stepSample } from "../chartHover";

const TIMES = [1000, 1060, 1120, 1180, 1240];

describe("nearestIndex", () => {
  it("finds the sample nearest a time, ties going to the earlier one", () => {
    expect(nearestIndex(TIMES, 1000)).toBe(0);
    expect(nearestIndex(TIMES, 1089)).toBe(1);
    expect(nearestIndex(TIMES, 1090)).toBe(1);
    expect(nearestIndex(TIMES, 1091)).toBe(2);
    expect(nearestIndex(TIMES, 1240)).toBe(4);
  });

  it("clamps to the ends outside the samples' span", () => {
    expect(nearestIndex(TIMES, 0)).toBe(0);
    expect(nearestIndex(TIMES, 99_999)).toBe(4);
  });

  it("has no sample to find in an empty series or at a non-finite time", () => {
    expect(nearestIndex([], 1000)).toBeNull();
    expect(nearestIndex(TIMES, Number.NaN)).toBeNull();
  });
});

describe("indexAtFraction", () => {
  it("reads the sample at a fraction of the plot's width across its extent", () => {
    expect(indexAtFraction(TIMES, [1000, 1240], 0)).toBe(0);
    expect(indexAtFraction(TIMES, [1000, 1240], 0.5)).toBe(2);
    expect(indexAtFraction(TIMES, [1000, 1240], 1)).toBe(4);
    expect(indexAtFraction(TIMES, [1000, 1240], 1.7)).toBe(4);
    expect(indexAtFraction(TIMES, [1000, 1240], -1)).toBe(0);
  });
});

describe("stepSample", () => {
  const has = (i: number) => i !== 2; // sample 2 has no value in any series

  it("starts at the latest sample from no highlight, whichever arrow", () => {
    expect(stepSample(null, "ArrowLeft", 5, has)).toBe(4);
    expect(stepSample(null, "ArrowRight", 5, has)).toBe(4);
  });

  it("steps to the neighbouring sample that has a value, skipping gaps, and stops at the ends", () => {
    expect(stepSample(3, "ArrowLeft", 5, has)).toBe(1);
    expect(stepSample(1, "ArrowRight", 5, has)).toBe(3);
    expect(stepSample(0, "ArrowLeft", 5, has)).toBe(0);
    expect(stepSample(4, "ArrowRight", 5, has)).toBe(4);
  });

  it("jumps to the ends with Home and End, and clears with Escape", () => {
    expect(stepSample(3, "Home", 5, (i) => i > 0)).toBe(1);
    expect(stepSample(1, "End", 5, (i) => i < 4)).toBe(3);
    expect(stepSample(3, "Escape", 5, has)).toBeNull();
  });

  it("leaves every other key to the page", () => {
    expect(stepSample(3, "Tab", 5, has)).toBeUndefined();
    expect(stepSample(3, "ArrowUp", 5, has)).toBeUndefined();
    expect(stepSample(null, "ArrowLeft", 0)).toBeUndefined();
  });
});

describe("tooltip content", () => {
  const series = [
    { label: "Requests", values: [10, 12.5, null, 14, 15] },
    { label: "Requests (previous week)", values: [9, 9, 9, 9, 9], dashed: true },
  ];

  it("formats each series' value at the sample in the chart's unit, and says when one has none", () => {
    const rows = sampleRows(series, 1, "rate", (i) => `hsl(var(--chart-${i + 1}))`);
    expect(rows).toEqual([
      { label: "Requests", value: "12.5/s", color: "hsl(var(--chart-1))", dashed: undefined },
      { label: "Requests (previous week)", value: "9/s", color: "hsl(var(--chart-2))", dashed: true },
    ]);
    expect(sampleRows(series, 2, "rate", () => "x")[0]!.value).toBe("—");
  });

  it("uses the axis' own formatter: a percent is a percent, a duration a duration", () => {
    expect(sampleRows([{ label: "Errors", values: [0.034] }], 0, "percent", () => "x")[0]!.value).toBe("3.4%");
    expect(sampleRows([{ label: "p95", values: [1250] }], 0, "ms", () => "x")[0]!.value).toBe("1.25s");
  });

  it("announces a lone series by its value, and several by name", () => {
    const one = sampleRows([series[0]!], 0, "rate", () => "x");
    expect(describeSample("Nov 18, 03:30 PM", one)).toBe(`Nov 18, 03:30 PM: ${one[0]!.value}.`);
    const both = sampleRows(series, 0, "rate", () => "x");
    expect(describeSample("Nov 18, 03:30 PM", both)).toBe(`Nov 18, 03:30 PM: Requests ${both[0]!.value}, Requests (previous week) ${both[1]!.value}.`);
  });

  it("heads an instant with its date and time, and a calendar bucket with its UTC date", () => {
    const sec = Date.UTC(2026, 10, 18, 23, 30) / 1000;
    expect(formatSampleTime(sec)).toBe(new Date(sec * 1000).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }));
    const day = Date.UTC(2026, 10, 18) / 1000;
    expect(formatSampleTime(day, { timezone: "UTC" })).toBe(new Date(day * 1000).toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric", timeZone: "UTC" }));
  });
});

describe("placeTooltip", () => {
  const viewport = { width: 1000, height: 800 };
  const size = { width: 160, height: 60 };

  it("sits right of the crosshair, centred on the plot", () => {
    expect(placeTooltip({ x: 300, top: 100, bottom: 140 }, size, viewport)).toEqual({ left: 312, top: 90, side: "right" });
  });

  it("flips left of the crosshair at the viewport's right edge, never covering the sample", () => {
    const placed = placeTooltip({ x: 900, top: 100, bottom: 140 }, size, viewport);
    expect(placed.side).toBe("left");
    expect(placed.left + size.width).toBeLessThan(900);
  });

  it("stays inside the viewport at the top and bottom", () => {
    expect(placeTooltip({ x: 300, top: 0, bottom: 10 }, size, viewport).top).toBe(8);
    expect(placeTooltip({ x: 300, top: 790, bottom: 800 }, size, viewport).top).toBe(800 - 8 - 60);
  });
});

describe("slotIndexAt and slotAnchor (a bar chart's buckets, a heatmap's columns)", () => {
  const rect = { left: 100, width: 200, top: 10, bottom: 60 };

  it("reads the slot under the pointer, clamped to the first and last", () => {
    // Four slots, 50px each.
    expect(slotIndexAt(rect, 100, 4)).toBe(0);
    expect(slotIndexAt(rect, 149, 4)).toBe(0);
    expect(slotIndexAt(rect, 150, 4)).toBe(1);
    expect(slotIndexAt(rect, 299, 4)).toBe(3);
    expect(slotIndexAt(rect, 40, 4)).toBe(0);
    expect(slotIndexAt(rect, 900, 4)).toBe(3);
  });

  it("has no slot without samples or width", () => {
    expect(slotIndexAt(rect, 150, 0)).toBeNull();
    expect(slotIndexAt({ left: 0, width: 0 }, 0, 4)).toBeNull();
  });

  it("anchors the tooltip on the slot's centre, the plot's top and bottom", () => {
    expect(slotAnchor(rect, 1, 4)).toEqual({ x: 175, top: 10, bottom: 60 });
    expect(slotAnchor(rect, 4, 4)).toBeNull();
  });
});
