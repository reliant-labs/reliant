import { describe, expect, it } from "vitest";

import {
  asOfLabel,
  burnLabel,
  machinePricingFootnote,
  planOwnSizeHours,
  remainingHeadline,
  runsOutLabel,
  sizeHours,
  storageCostLabel,
  storageCard,
  projectedBillLabel,
  hoursRanOut,
  type DaemonPricingLike,
  type DaemonUsageLike,
} from "../daemonUsage";

/**
 * Per-daemon billing task J (design §6.2). Every sentence a user reads about
 * their small-daemon-hours is pinned here, built ONLY from the server's §6.1
 * fields and ListPlans' daemon_pricing — the client restates no multiplier,
 * price or disk size.
 */

const pricing: DaemonPricingLike = {
  pricingVersion: "test-v1",
  sizes: [
    { size: "small", costWeight: "1/1", hourlyPriceCents: "13.0000", storageGib: 25n },
    { size: "medium", costWeight: "2/1", hourlyPriceCents: "26.0000", storageGib: 50n },
    { size: "large", costWeight: "4/1", hourlyPriceCents: "52.0000", storageGib: 100n },
    { size: "xl", costWeight: "8/1", hourlyPriceCents: "104.0000", storageGib: 200n },
    { size: "2xl", costWeight: "16/1", hourlyPriceCents: "208.0000", storageGib: 400n },
  ],
  storageCentsPerGibMonth: "25.0000",
  placeholder: true,
};

function usage(over: Partial<DaemonUsageLike> = {}): DaemonUsageLike {
  return {
    usageMeasured: true,
    pricingVersion: "test-v1",
    allowanceWeightedSeconds: "576000/1",
    consumedWeightedSeconds: "0/1",
    remainingWeightedSeconds: "0/1",
    grantRemainingWeightedSeconds: "0/1",
    runningCostWeightSum: "0/1",
    bySize: [],
    ...over,
  };
}

const ts = (d: Date) => ({ seconds: BigInt(Math.floor(d.getTime() / 1000)) });

describe("usage headline", () => {
  it("reads remaining SDS as hours", () => {
    expect(remainingHeadline(usage({ remainingWeightedSeconds: "149400/1", runningCostWeightSum: "1/1" }))).toBe(
      "≈ 41.5 h left at the current running-machine rate",
    );
  });

  // usage_measured=false still renders as UNKNOWN, never "0.0 … left".
  it("renders nothing when the server did not measure", () => {
    expect(remainingHeadline(usage({ usageMeasured: false }))).toBeNull();
    expect(burnLabel(usage({ usageMeasured: false, runningCostWeightSum: "4/1" }))).toBeNull();
    expect(runsOutLabel(usage({ usageMeasured: false, exhaustsAt: ts(new Date()) }))).toBeNull();
    expect(asOfLabel(usage({ usageMeasured: false, measuredThrough: ts(new Date()) }))).toBeNull();
    expect(remainingHeadline(undefined)).toBeNull();
  });
});

describe("burn line", () => {
  it("names the size when the period ran exactly one and it is up", () => {
    const u = usage({
      remainingWeightedSeconds: "149760/1",
      runningCostWeightSum: "4/1",
      bySize: [{ size: "large", pricingVersion: "test-v1", costWeight: "4/1", exactRunningSeconds: "100/1", exactOverageSeconds: "0/1" }],
    });
    expect(burnLabel(u)).toBe("≈ 10.4 h left at the current running-machine rate");
  });

  it("does not guess a size when two are running", () => {
    const u = usage({
      remainingWeightedSeconds: "36000/1",
      runningCostWeightSum: "5/1",
      bySize: [
        { size: "small", pricingVersion: "test-v1", costWeight: "1/1", exactRunningSeconds: "10/1", exactOverageSeconds: "0/1" },
        { size: "large", pricingVersion: "test-v1", costWeight: "4/1", exactRunningSeconds: "10/1", exactOverageSeconds: "0/1" },
      ],
    });
    expect(burnLabel(u)).toBe("≈ 2.0 h left at the current running-machine rate");
  });

  it("is absent when nothing is running", () => {
    expect(burnLabel(usage({ remainingWeightedSeconds: "3600/1" }))).toBeNull();
  });
});

describe("runs out / as of", () => {
  const now = new Date("2026-10-05T12:00:00Z");

  it("renders exhausts_at when present", () => {
    const at = new Date("2026-10-08T09:00:00Z"); // a Thursday, 3 days out
    expect(runsOutLabel(usage({ exhaustsAt: ts(at) }), now)).toMatch(/^runs out ~Thu at the current rate$/);
  });

  // exhausts_at is UNSET when nothing is running — the label must not invent one.
  it("is absent when the server sent no exhausts_at", () => {
    expect(runsOutLabel(usage(), now)).toBeNull();
  });

  it("renders measured_through as a clock time", () => {
    expect(asOfLabel(usage({ measuredThrough: ts(new Date()) }))).toMatch(/^as of \d{2}:\d{2}$/);
  });
});

describe("hours in the footnote", () => {
  it("converts the plan's small hours into hours of a size", () => {
    for (const [size, hrs] of [["small", "160.0"], ["medium", "80.0"], ["large", "40.0"], ["xl", "20.0"], ["2xl", "10.0"]] as const) {
      expect(sizeHours(pricing, size, "576000/1")).toBe(hrs);
    }
  });

  it("never invents an allowance", () => {
    expect(sizeHours(pricing, "small", undefined)).toBeNull();
    expect(sizeHours(pricing, "small", "NaN")).toBeNull();
    expect(sizeHours(undefined, "small", "576000/1")).toBeNull();
    expect(sizeHours(pricing, "unknown", "576000/1")).toBeNull();
    expect(sizeHours(pricing, "small", "0/1")).toBe("0.0");
  });

  it("rounds down rather than promise more than the plan includes", () => {
    const p: DaemonPricingLike = { ...pricing, sizes: [{ ...pricing.sizes[0], costWeight: "3/1" }] };
    expect(sizeHours(p, "small", "360000/1")).toBe("33.3");
  });

  // A 2XL plan holds 2,560 small hours; the user is told 160 hours of 2XL.
  it("states a plan's hours at its OWN (largest allowed) size, never in small units", () => {
    expect(planOwnSizeHours(pricing, ["small"], "576000/1")).toBe("160.0");
    expect(planOwnSizeHours(pricing, ["small", "medium"], "576000/1")).toBe("80.0");
    expect(planOwnSizeHours(pricing, ["small", "medium", "large", "xl", "2xl"], "9216000/1")).toBe("160.0");
    expect(planOwnSizeHours(pricing, [], "576000/1")).toBeNull();
    expect(planOwnSizeHours(pricing, ["small"], undefined)).toBeNull();
  });

  it("prices storage from the server's per-GiB price", () => {
    expect(storageCostLabel(pricing, 25)).toBe("$6.25/mo");
    expect(storageCostLabel(pricing, 0)).toBeNull();
  });

  it("shows storage as one number, with the included amount and any extra cost", () => {
    expect(storageCard({ provisionedGib: 60n, includedGib: 60n, extraGib: 0n, costCents: "0", projectedCostCents: "0" }))
      .toEqual({ value: "60 GB", detail: "60 GB included", extraCost: null });
    expect(storageCard({ provisionedGib: 120n, includedGib: 60n, extraGib: 60n, costCents: "500", projectedCostCents: "1500.0000" })?.extraCost)
      .toBe("$15.00 this period for 60 GB extra");
    expect(storageCard(undefined)).toBeNull();
  });

  it("projects the bill only from a measured response", () => {
    expect(projectedBillLabel({ usageMeasured: true, projectedPeriodTotalCents: "2740.5000" })).toBe("$27.41");
    expect(projectedBillLabel({ usageMeasured: false, projectedPeriodTotalCents: "2740" })).toBeNull();
    expect(projectedBillLabel({ usageMeasured: true })).toBeNull();
  });

  it("calls machines paused only when hours, coupon hours and overage are all gone", () => {
    expect(hoursRanOut(usage({ remainingWeightedSeconds: "0/1" }), false)).toBe(true);
    expect(hoursRanOut(usage({ remainingWeightedSeconds: "0/1" }), true)).toBe(false);
    expect(hoursRanOut(usage({ remainingWeightedSeconds: "3600/1" }), false)).toBe(false);
    expect(hoursRanOut({ ...usage({ remainingWeightedSeconds: "0/1" }), grantRemainingWeightedSeconds: "60/1" }, false)).toBe(false);
    expect(hoursRanOut(usage({ usageMeasured: false }), false)).toBe(false);
  });
});

describe("footnote under every picker", () => {
  it("is the owner's two sentences, with no rates, disks or fees", () => {
    const line = machinePricingFootnote("160");
    expect(line).toBe("Includes 160 hours a month. Extra hours and storage are billed separately.");
    expect(line).not.toMatch(/\$|GiB|GB|×|suspended|small-daemon/i);
  });

  it("drops the hours clause rather than guess it", () => {
    expect(machinePricingFootnote(null)).toBe("Extra hours and storage are billed separately.");
  });
});
