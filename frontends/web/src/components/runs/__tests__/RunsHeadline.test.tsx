// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import type { RunSummary } from "@/hooks/run-queries";

import { runsHeadlineFigures, runsSeries } from "../RunsHeadline";

const HOUR = 3_600_000;
const NOW = Date.UTC(2026, 10, 19, 12, 30);

const run = (displayState: RunDisplayState, createdAt = NOW - HOUR, outcome = ""): RunSummary =>
  ({ chatId: `c-${Math.random()}`, displayState, outcome, createdAt }) as unknown as RunSummary;

describe("runsHeadlineFigures", () => {
  it("counts the runs, and the success rate over the finished ones only", () => {
    const figures = runsHeadlineFigures([
      run(RunDisplayState.COMPLETED),
      run(RunDisplayState.COMPLETED),
      run(RunDisplayState.COMPLETED, NOW, "failure"),
      run(RunDisplayState.FAILED),
      run(RunDisplayState.RUNNING),
      run(RunDisplayState.NEEDS_INPUT),
      run(RunDisplayState.CANCELLED),
    ]);
    expect(figures.started).toBe(7);
    // Two succeeded of four that finished with a verdict; cancelled is neither.
    expect(figures.successRate).toBe(0.5);
    expect(figures.live).toBe(1);
    expect(figures.needsYou).toBe(1);
  });

  it("has no success rate when nothing finished", () => {
    expect(runsHeadlineFigures([run(RunDisplayState.RUNNING)]).successRate).toBeNull();
    expect(runsHeadlineFigures([]).started).toBe(0);
  });
});

describe("runsSeries", () => {
  it("buckets a day by hour, and every run lands in one bucket", () => {
    const runs = [run(RunDisplayState.COMPLETED, NOW - 10 * 60_000), run(RunDisplayState.COMPLETED, NOW - 5 * HOUR), run(RunDisplayState.COMPLETED, NOW - 5 * HOUR)];
    const series = runsSeries(runs, "24h", NOW);
    expect(series.values).toHaveLength(24);
    expect(series.values.reduce((a, b) => a + b, 0)).toBe(3);
    expect(series.values[series.values.length - 1]).toBe(1);
    expect(Math.max(...series.values)).toBe(2);
  });

  it("buckets a week by day and leaves out runs older than the window", () => {
    const series = runsSeries([run(RunDisplayState.COMPLETED, NOW - 2 * 24 * HOUR), run(RunDisplayState.COMPLETED, NOW - 9 * 24 * HOUR)], "7d", NOW);
    expect(series.values).toHaveLength(7);
    expect(series.values.reduce((a, b) => a + b, 0)).toBe(1);
  });
});
