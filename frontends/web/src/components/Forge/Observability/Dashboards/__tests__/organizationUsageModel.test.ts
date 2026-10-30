// Copyright (c) 2026 Reliant Labs
import { describe, expect, it } from "vitest";

import {
  allowanceRows,
  bucketStarts,
  categoryAmount,
  formatQuantity,
  formatUsd,
  groupTotals,
  projectedMonthEnd,
  spendSeries,
  usageWindow,
} from "../organizationUsageModel";
import { organizationCostFixture } from "./organizationCostFixtures";
import { NOW_MS, usageByCategory, usageByEnv, usageUnavailable } from "./organizationUsageFixtures";

describe("organization usage model", () => {
  it("formats exact dollars from nanos, keeps sub-cent charges visible, and says n/a for absent", () => {
    expect(formatUsd(12_345_000_000n)).toBe("$12.35");
    expect(formatUsd(1_234_567_890_123n)).toBe("$1,234.57");
    expect(formatUsd(0n)).toBe("$0.00");
    expect(formatUsd(4_000_000n)).toBe("<$0.01");
    expect(formatUsd(-2_500_000_000n)).toBe("-$2.50");
    expect(formatUsd(undefined)).toBe("n/a");
  });

  it("opens every preset on more than one UTC bucket ending after today", () => {
    expect(usageWindow("7d", NOW_MS)).toEqual({ startMs: Date.parse("2026-10-19T00:00:00Z"), endMs: Date.parse("2026-10-26T00:00:00Z"), granularity: "day" });
    expect(usageWindow("12mo", NOW_MS)).toEqual({ startMs: Date.parse("2025-11-01T00:00:00Z"), endMs: Date.parse("2026-11-01T00:00:00Z"), granularity: "month" });
    expect(bucketStarts(usageByCategory())).toHaveLength(30);
  });

  it("tells an amount from unpriced usage, no usage and an unavailable source", () => {
    const usage = usageByCategory();
    expect(categoryAmount(usage, "managed_llm")).toEqual({ kind: "amount", nanos: 12_344_000_000n, basis: "actual", unpricedDimensions: [] });
    expect(categoryAmount(usage, "hosted_apps")).toEqual({ kind: "amount", nanos: 4_000_000_000n, basis: "list_equivalent", unpricedDimensions: ["infra_memory"] });
    expect(categoryAmount(usage, "machines")).toEqual({ kind: "unpriced" });
    expect(categoryAmount(usage, "static_hosting")).toEqual({ kind: "unpriced" });
    expect(categoryAmount(usage, "hosted_other")).toEqual({ kind: "no-usage" });
    expect(categoryAmount(usage, "observability")).toEqual({ kind: "unavailable" });
    expect(categoryAmount(usageUnavailable(), "managed_llm")).toEqual({ kind: "unavailable" });
  });

  it("draws a measured zero inside a source's window and a gap outside it, one basis per chart", () => {
    const usage = usageByCategory();
    const estimate = spendSeries(usage, "list_equivalent");
    expect(estimate.map((s) => s.key)).toEqual(["hosted_apps", "databases"]);
    const apps = estimate[0]!.values;
    // Hosted infrastructure covers 2026-10-15 onward: before it the bucket is unknown.
    expect(apps[0]).toBeNull();
    expect(apps[19]).toBe(0); // 2026-10-15
    expect(apps[24]).toBe(4); // 2026-10-20
    expect(spendSeries(usage, "actual").map((s) => s.key)).toEqual(["managed_llm"]);
    expect(spendSeries(usageUnavailable(), "actual")).toEqual([]);
  });

  it("totals groups by key and labels the unattributed", () => {
    const totals = groupTotals(usageByEnv(), ["hosted_apps", "databases"]);
    expect(totals.map((t) => [t.label, t.category, t.nanos])).toEqual([
      ["prod", "hosted_apps", 3_000_000_000n],
      ["prod", "databases", 1_000_000_000n],
      ["staging", "hosted_apps", 1_000_000_000n],
    ]);
  });

  it("projects to month end only after 24 measured hours", () => {
    const rate = { category: "managed_llm", basis: "actual", averageDaily7dUsdNanos: 2_000_000_000n, measuredHours: 168 } as Parameters<typeof projectedMonthEnd>[1];
    expect(projectedMonthEnd(20_000_000_000n, rate, NOW_MS)).toBe(33_000_000_000n);
    expect(projectedMonthEnd(20_000_000_000n, { ...rate!, measuredHours: 23 }, NOW_MS)).toBeUndefined();
    expect(projectedMonthEnd(undefined, rate, NOW_MS)).toBeUndefined();
  });

  it("projects allowances straight-line to their period end", () => {
    const [cpu] = allowanceRows(organizationCostFixture(), NOW_MS, ["hosted_infrastructure"]);
    // 12.5 used after 24.5 of 31 days.
    expect(cpu).toMatchObject({ id: "cpu", included: 100, consumed: 12.5 });
    expect(cpu!.projected).toBeCloseTo((12.5 * 31) / 24.5, 5);
  });

  it("names native quantities", () => {
    expect(formatQuantity(7200, "seconds")).toBe("2 h");
    expect(formatQuantity(12.5, "vcpu_hours")).toBe("12.5 vCPU-h");
    expect(formatQuantity(undefined, "kib")).toBe("n/a");
  });
});
