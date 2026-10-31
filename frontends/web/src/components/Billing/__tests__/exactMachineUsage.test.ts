import { describe, expect, it } from "vitest";
import { burnLabel, equivalentHours, hoursRanOut, measuredMachineUsage, projectedBillLabel, usageWindowLabel, type DaemonUsageLike } from "../daemonUsage";
import { exactUsageDisplay } from "../exactMachineUsage";
const usage: DaemonUsageLike = { usageMeasured: true, pricingVersion: "v1", allowanceWeightedSeconds: "576000/1", consumedWeightedSeconds: "1/3", remainingWeightedSeconds: "1727999/3", grantRemainingWeightedSeconds: "0/1", runningCostWeightSum: "207341557/145891252", bySize: [{ size: "medium", pricingVersion: "v1", costWeight: "207341557/145891252", exactRunningSeconds: "0/1", exactOverageSeconds: "0/1" }] };
describe("versioned weighted machine display", () => {
  it("requires exact fields/version rather than falling back to deprecated zero defaults", () => {
    expect(measuredMachineUsage({ usageMeasured: true, bySize: [] })).toBe(false);
    expect(exactUsageDisplay({ ...usage, billingHoldReason: "route unknown" })).toBeNull();
    expect(exactUsageDisplay({ ...usage, remainingWeightedSeconds: "NaN" })).toBeNull();
    expect(exactUsageDisplay(usage)?.includedLabel).toBe("160.0 weighted h included");
  });
  it("converts selected-size equivalents by exact weights, independent of retail rates", () => {
    expect(equivalentHours(usage, "207341557/145891252", "allowanceWeightedSeconds")).toBe("112.5");
    expect(burnLabel(usage)).toContain("≈ 112.5 h left at the current");
    expect(hoursRanOut({ ...usage, remainingWeightedSeconds: "0/1" }, false)).toBe(true);
    expect(hoursRanOut({ ...usage, remainingWeightedSeconds: "1/999999999" }, false)).toBe(false);
  });
  it("labels partial measured windows and never projects a bill from one", () => {
    const partial = { ...usage, periodStart: { seconds: 100n }, measuredFrom: { seconds: 200n } };
    expect(usageWindowLabel(partial)).toContain("earlier use this period is not shown");
    expect(usageWindowLabel(partial)).not.toContain("this billing period");
    expect(projectedBillLabel({ ...partial, projectedPeriodTotalCents: "6900/1" })).toBeNull();
  });
});
