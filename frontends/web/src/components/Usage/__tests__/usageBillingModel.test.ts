// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import type { ProjectCost } from "@/services/controlPlane/observability/organizationCostsApi";
import type { OrganizationUsage } from "@/services/controlPlane/observability/organizationUsageApi";

import {
  DEFAULT_USAGE_RANGE,
  listEstimateTotal,
  projectAmountText,
  projectCostRow,
  usageCategoryLabel,
  usageRangeOf,
} from "../usageBillingModel";

const usage = (points: Partial<OrganizationUsage["points"][number]>[]) =>
  ({ points: points.map((p) => ({ unpricedDimensions: [], ...p })) }) as unknown as OrganizationUsage;

describe("usageBillingModel", () => {
  it("reads the period from the URL, defaulting anything unknown", () => {
    expect(usageRangeOf("90d")).toBe("90d");
    expect(usageRangeOf("forever")).toBe(DEFAULT_USAGE_RANGE);
    expect(usageRangeOf(undefined)).toBe(DEFAULT_USAGE_RANGE);
  });

  it("names machines Agent sandboxes", () => {
    expect(usageCategoryLabel("machines")).toBe("Agent sandboxes");
    expect(usageCategoryLabel("databases")).toBe("Databases");
  });

  it("totals list-price points only, never adding settled charges", () => {
    const total = listEstimateTotal(
      usage([
        { category: "hosted_apps", listEquivalentUsdNanos: 2_000_000_000n },
        { category: "databases", listEquivalentUsdNanos: 500_000_000n, unpricedDimensions: ["egress_kib"] },
        { category: "managed_llm", actualChargesUsdNanos: 9_000_000_000n },
      ]),
    );
    expect(total).toEqual({ nanos: 2_500_000_000n, unpricedDimensions: ["egress_kib"] });
  });

  it("has no total when nothing was priced: unknown, not zero", () => {
    expect(listEstimateTotal(usage([{ category: "machines" }]))).toBeUndefined();
  });

  it("says n/a for an unmeasured project amount and marks a lower bound", () => {
    expect(projectAmountText(undefined)).toBe("n/a");
    expect(projectAmountText({ usdNanos: 12_340_000_000n, lowerBound: true } as never)).toBe("≥ $12.34");
  });

  it("finds the category that costs a project the most", () => {
    const cost = {
      monthToDate: { usdNanos: 10_000_000_000n },
      forecast: { amount: { usdNanos: 30_000_000_000n } },
      daily: [
        { category: "compute", list: { usdNanos: 6_000_000_000n } },
        { category: "storage", list: { usdNanos: 1_000_000_000n } },
        { category: "compute", list: { usdNanos: 2_000_000_000n } },
        { category: "bandwidth", list: undefined },
      ],
    } as unknown as ProjectCost;
    const row = projectCostRow("shopfront", cost);
    expect(row.top).toEqual({ category: "compute", share: 8 / 9 });
    expect(row.forecast?.usdNanos).toBe(30_000_000_000n);
  });
});
