// Copyright (c) 2026 Reliant Labs
import { describe, expect, it } from "vitest";
import { costCompletenessLabel, costTimestamp, formatCostUSD, organizationCostGroups } from "../organizationCostModel";
import { organizationCostFixture } from "./organizationCostFixtures";

describe("cost display model", () => {
  it.each([
    [undefined, "Not reported"],
    [0n, "USD 0.00"],
    [1n, "USD 0.000000001"],
    [-1n, "USD -0.000000001"],
    [1_234_560_000n, "USD 1.23456"],
    [-2_500_000_000n, "USD -2.50"],
    [9_223_372_036_854_775_807n, "USD 9,223,372,036.854775807"],
  ])("formats %s as %s without unsafe Number conversion", (input, expected) => {
    expect(formatCostUSD(input)).toBe(expected);
  });

  it("does not invent a timestamp or coverage for unknown data", () => {
    expect(costTimestamp(undefined)).toBe("Not reported");
    expect(costTimestamp({ seconds: 0n, nanos: 0 })).toBe("1970-01-01 00:00:00 UTC");
    expect(costCompletenessLabel("future-value")).toBe("Coverage not reported");
  });

  it("groups rather than sums and keeps unknown period sources", () => {
    const summary = organizationCostFixture();
    const source = summary.sources[0]!;
    summary.sources.push({ ...source, periodId: "new_period" });
    const groups = organizationCostGroups(summary);
    expect(groups.map((g) => g.id)).toEqual(["month_to_date", "compute_billing_period", "new_period"]);
    expect(groups[2]!.period).toBeUndefined();
    expect(groups[2]!.sources).toEqual([{ ...source, periodId: "new_period" }]);
    expect(groups.flatMap((g) => g.sources)).toHaveLength(summary.sources.length);
  });
});
