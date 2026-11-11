// Copyright (c) 2026 Reliant Labs

/**
 * The Overview's Traces tile counted the page it loaded ("100 in the last 24
 * hours") instead of the window. The count is the server's total when it sends
 * one, and "100+" when the page has more behind it.
 */
import { describe, expect, it } from "vitest";

import type { TraceSummary } from "@/services/controlPlane/observability/types";
import { traceCountLabel } from "../SignalTiles";

const rows = (n: number) => Array.from({ length: n }, () => ({}) as TraceSummary);
const red = { traceCount: 1234, errorTraceCount: 9, ratePerSecond: 1, errorRate: 0.007, p50Ms: 10, p95Ms: 200, p99Ms: 900 };

describe("traceCountLabel", () => {
  it("is the window's total from the server, not the loaded page's length", () => {
    expect(traceCountLabel({ traces: rows(100), nextCursor: "c", red })).toBe("1.2k");
  });

  it("says 100+ when the page is capped and no total came back", () => {
    expect(traceCountLabel({ traces: rows(100), nextCursor: "c", red: null })).toBe("100+");
  });

  it("is the exact count when the page holds every trace", () => {
    expect(traceCountLabel({ traces: rows(42), nextCursor: "", red: null })).toBe("42");
  });

  it("says nothing before anything loaded", () => {
    expect(traceCountLabel(undefined)).toBe("");
  });
});
