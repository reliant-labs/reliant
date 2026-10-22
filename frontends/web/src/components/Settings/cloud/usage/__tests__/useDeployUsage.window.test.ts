// The attribution window ListUsage is asked for. control-plane refuses a window
// over 31 days and retains raw ticks for 7, so the window is the billing period
// clamped to the retained week: never wider (a refused request), never in the
// future (nothing is metered there).

import { describe, expect, it } from "vitest";

import {
  USAGE_RETENTION_MS,
  attributionWindow,
} from "../useDeployUsage";

const now = new Date("2026-09-23T12:00:00Z");
const days = (n: number) => n * 24 * 60 * 60 * 1000;

describe("attributionWindow", () => {
  it("uses the period as-is when it is within the retained week", () => {
    const start = new Date(now.getTime() - days(3));
    const { start: s, end: e } = attributionWindow(start, now, now);
    expect(s).toEqual(start);
    expect(e).toEqual(now);
  });

  it("clamps a month-long period to the retained week ending now", () => {
    const periodStart = new Date(now.getTime() - days(20));
    const periodEnd = new Date(now.getTime() + days(10));
    const { start, end } = attributionWindow(periodStart, periodEnd, now);
    expect(end).toEqual(now);
    expect(end.getTime() - start.getTime()).toBe(USAGE_RETENTION_MS);
  });

  it("ends at a period that already closed, not at now", () => {
    const periodEnd = new Date(now.getTime() - days(1));
    const periodStart = new Date(periodEnd.getTime() - days(2));
    const { start, end } = attributionWindow(periodStart, periodEnd, now);
    expect(end).toEqual(periodEnd);
    expect(start).toEqual(periodStart);
  });

  it("falls back to the retained week when there is no period (no plan)", () => {
    const { start, end } = attributionWindow(undefined, undefined, now);
    expect(end).toEqual(now);
    expect(end.getTime() - start.getTime()).toBe(USAGE_RETENTION_MS);
  });
});
