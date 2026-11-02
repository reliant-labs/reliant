// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { TraceSummary } from "@/services/controlPlane/observability/types";

import { spreadLabels } from "../TraceLatencyChart";
import {
  histogram,
  inRegion,
  latencyDomain,
  linearScale,
  linearTicks,
  logScale,
  logTicks,
  nearestPoint,
  niceCeil,
  percentiles,
  pointsOf,
  regionOf,
  timeDomain,
  timeTicks,
  type TracePoint,
} from "../traceScatterModel";

const point = (traceId: string, startMs: number, durationMs: number, error = false): TracePoint => ({
  traceId,
  startMs,
  durationMs,
  error,
  label: traceId,
});

describe("pointsOf", () => {
  it("plots the root span's duration and fails a trace with any error span, as the list does", () => {
    const trace: TraceSummary = {
      traceId: "t1",
      rootService: "api",
      rootOperation: "GetOrder",
      startMs: 1000,
      durationMs: 900,
      rootDurationMs: 120,
      spanCount: 4,
      errorCount: 1,
      services: ["api"],
      source: "",
      operatorData: false,
    } as TraceSummary;
    expect(pointsOf([trace])).toEqual([{ traceId: "t1", startMs: 1000, durationMs: 120, error: true, label: "api GetOrder" }]);
  });
});

describe("scales", () => {
  it("maps a linear domain onto pixels and back", () => {
    const s = linearScale([0, 100], [200, 0]);
    expect(s.toPx(0)).toBe(200);
    expect(s.toPx(100)).toBe(0);
    expect(s.fromPx(100)).toBe(50);
  });

  it("maps a log domain by decade, and puts a value under the floor on the floor", () => {
    const s = logScale([1, 1000], [300, 0]);
    expect(s.toPx(10)).toBeCloseTo(200);
    expect(s.toPx(100)).toBeCloseTo(100);
    expect(s.fromPx(200)).toBeCloseTo(10);
    expect(s.toPx(0)).toBe(300);
  });
});

describe("latencyDomain", () => {
  it("starts a linear axis at zero and ends it on a round number above the slowest trace", () => {
    expect(latencyDomain([12, 480], "linear")).toEqual([0, 1000]);
    expect(latencyDomain([], "linear")).toEqual([0, 1]);
  });

  it("spans the decades around the data on a log axis, ignoring zero-length traces", () => {
    expect(latencyDomain([0, 3, 450], "log")).toEqual([1, 1000]);
    expect(latencyDomain([100], "log")).toEqual([100, 1000]);
    expect(latencyDomain([0], "log")).toEqual([1, 10]);
    expect(latencyDomain([0.0001, 5], "log")).toEqual([0.01, 10]);
  });

  it("rounds up to 1, 2, 2.5 or 5 times a power of ten", () => {
    expect(niceCeil(0.7)).toBe(1);
    expect(niceCeil(140)).toBe(200);
    expect(niceCeil(2100)).toBe(2500);
    expect(niceCeil(4800)).toBe(5000);
  });
});

describe("ticks", () => {
  it("steps a linear axis by round values", () => {
    expect(linearTicks([0, 1000], 4)).toEqual([0, 250, 500, 750, 1000]);
  });

  it("labels each decade of a wide log axis, and the 2s and 5s of a narrow one", () => {
    expect(logTicks([1, 10_000])).toEqual([1, 10, 100, 1000, 10_000]);
    expect(logTicks([10, 1000])).toEqual([10, 100, 1000]);
    expect(logTicks([10, 100])).toEqual([10, 20, 50, 100]);
  });

  it("puts time ticks on round local instants inside the domain", () => {
    const lo = new Date(2026, 9, 25, 14, 3, 0).getTime();
    const hi = lo + 60 * 60_000;
    const { ticks, stepMs } = timeTicks([lo, hi], 5);
    expect(stepMs).toBe(15 * 60_000);
    expect(ticks.map((t) => new Date(t).getMinutes())).toEqual([15, 30, 45, 0]);
    for (const t of ticks) expect(t >= lo && t <= hi).toBe(true);
  });
});

describe("timeDomain", () => {
  const window = { startMs: 0, endMs: 3_600_000 };

  it("spans the window when every matching trace is loaded", () => {
    expect(timeDomain([point("a", 3_000_000, 1)], window, true)).toEqual([0, 3_600_000]);
  });

  it("starts near the oldest loaded trace when older ones are not loaded, so the gap does not read as no traffic", () => {
    const [lo, hi] = timeDomain([point("a", 3_000_000, 1), point("b", 3_500_000, 1)], window, false);
    expect(hi).toBe(3_600_000);
    expect(lo).toBeLessThan(3_000_000);
    expect(lo).toBeGreaterThan(2_900_000);
  });
});

describe("histogram", () => {
  it("counts successes and failures per bucket, evenly spaced on the axis", () => {
    const points = [point("a", 0, 5), point("b", 0, 15, true), point("c", 0, 15), point("d", 0, 95)];
    const buckets = histogram(points, [0, 100], "linear", 10);
    expect(buckets).toHaveLength(10);
    expect(buckets[0]).toMatchObject({ lo: 0, hi: 10, ok: 1, error: 0 });
    expect(buckets[1]).toMatchObject({ ok: 1, error: 1 });
    expect(buckets[9]).toMatchObject({ ok: 1, error: 0 });
    expect(buckets.reduce((n, b) => n + b.ok + b.error, 0)).toBe(4);
  });

  it("gives each log bucket the same ratio, and keeps the slowest trace in the last one", () => {
    const buckets = histogram([point("a", 0, 1), point("b", 0, 1000)], [1, 1000], "log", 3);
    expect(buckets.map((b) => Math.round(b.hi))).toEqual([10, 100, 1000]);
    expect(buckets[0].ok).toBe(1);
    expect(buckets[2].ok).toBe(1);
  });
});

describe("percentiles", () => {
  it("takes nearest-rank p50, p95 and p99 of the plotted latencies", () => {
    const points = Array.from({ length: 100 }, (_, i) => point(String(i), 0, i + 1));
    expect(percentiles(points)).toEqual({ p50: 50, p95: 95, p99: 99 });
    expect(percentiles([])).toBeNull();
  });
});

describe("region", () => {
  it("normalizes a drag in any direction and selects the points inside it", () => {
    const region = regionOf({ timeMs: 200, latencyMs: 10 }, { timeMs: 100, latencyMs: 50 });
    expect(region).toEqual({ startMs: 100, endMs: 200, minMs: 10, maxMs: 50 });
    expect(inRegion(point("in", 150, 20), region)).toBe(true);
    expect(inRegion(point("late", 250, 20), region)).toBe(false);
    expect(inRegion(point("slow", 150, 60), region)).toBe(false);
  });
});

describe("nearestPoint", () => {
  const x = linearScale([0, 100], [0, 100]);
  const y = linearScale([0, 100], [100, 0]);

  it("finds the closest dot within the radius, and nothing beyond it", () => {
    const points = [point("a", 10, 90), point("b", 50, 50)];
    expect(nearestPoint(points, x, y, 52, 49)?.traceId).toBe("b");
    expect(nearestPoint(points, x, y, 80, 80)).toBeNull();
  });

  it("prefers a failure drawn over a success at the same spot", () => {
    const points = [point("ok", 50, 50), point("err", 50, 50, true)];
    expect(nearestPoint(points, x, y, 50, 50)?.traceId).toBe("err");
  });
});

describe("spreadLabels", () => {
  it("pushes close percentile labels apart and leaves distant ones at their rules", () => {
    expect(spreadLabels([100, 20, 24])).toEqual([100, 20, 31]);
  });
});
