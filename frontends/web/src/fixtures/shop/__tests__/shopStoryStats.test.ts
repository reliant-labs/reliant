// Copyright (c) 2025 Reliant Labs

/**
 * demoStats is memoized per window and its quantiles are computed on demand
 * (shopStory.ts statsOf): the cache must be invisible. A count-only read, a
 * single quantile and the full answer agree, and no caller can change what
 * the next one reads.
 */

import { describe, expect, it } from "vitest";

import { demoCounts, demoQuantile, demoStats, demoTrend, HOUR } from "../shopStory";

const NOW = Date.UTC(2026, 10, 10, 15, 0, 0);
const window = { startMs: NOW - 6 * HOUR, endMs: NOW, envIds: ["env-prod"], service: "api", now: NOW };

describe("demoStats' cache", () => {
  it("answers counts, one quantile, and everything alike", () => {
    const whole = demoStats(window);
    const { p50Ms, p95Ms, p99Ms, ...counts } = whole;
    expect(demoCounts(window)).toEqual(counts);
    expect([demoQuantile(window, 0.5), demoQuantile(window, 0.95), demoQuantile(window, 0.99)]).toEqual([p50Ms, p95Ms, p99Ms]);
    expect(p50Ms).toBeLessThan(p95Ms);
    expect(p95Ms).toBeLessThan(p99Ms);
  });

  it("hands every caller its own copy", () => {
    const first = demoStats(window);
    const requests = first.requests;
    first.requests = -1;
    first.p50Ms = -1;
    demoCounts(window).errors = -1;
    expect(demoStats(window).requests).toBe(requests);
    expect(demoStats(window).p50Ms).toBeGreaterThan(0);
    expect(demoCounts(window).errors).toBeGreaterThanOrEqual(0);
  });

  it("keys on every part of the query", () => {
    const prod = demoCounts(window).requests;
    expect(demoCounts({ ...window, envIds: ["env-staging"] }).requests).toBeLessThan(prod);
    expect(demoCounts({ ...window, service: "worker" }).requests).not.toBe(prod);
    expect(demoCounts({ ...window, startMs: window.startMs + HOUR }).requests).toBeLessThan(prod);
    expect(demoCounts({ ...window, op: "orders.v1.OrdersService/CreateOrder" }).requests).toBeLessThan(prod);
  });

  it("splits a window into a trend that sums to its total", () => {
    const trend = demoTrend(window, 30);
    expect(trend).toHaveLength(30);
    expect(trend.reduce((a, b) => a + b.stats.requests, 0)).toBe(demoCounts(window).requests);
  });
});
