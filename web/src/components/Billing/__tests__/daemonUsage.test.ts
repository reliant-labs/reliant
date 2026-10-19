import { describe, expect, it } from "vitest";

import {
  asOfLabel,
  burnLabel,
  includedHoursSentence,
  remainingHeadline,
  runsOutLabel,
  sizeFacts,
  suspendedFeeLabel,
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
  sizes: [
    { size: "small", multiplier: 1n, hourlyPriceCents: "13.0000", storageGib: 25n },
    { size: "medium", multiplier: 2n, hourlyPriceCents: "26.0000", storageGib: 50n },
    { size: "large", multiplier: 4n, hourlyPriceCents: "52.0000", storageGib: 100n },
    { size: "xl", multiplier: 8n, hourlyPriceCents: "104.0000", storageGib: 200n },
    { size: "2xl", multiplier: 16n, hourlyPriceCents: "208.0000", storageGib: 400n },
  ],
  suspendedDiskCentsPerGibMonth: "25.0000",
  placeholder: true,
};

function usage(over: Partial<DaemonUsageLike> = {}): DaemonUsageLike {
  return {
    usageMeasured: true,
    includedSmallDaemonSeconds: 160n * 3600n,
    usedSmallDaemonSeconds: 0n,
    remainingSmallDaemonSeconds: 0n,
    runningMultiplier: 0n,
    bySize: [],
    ...over,
  };
}

const ts = (d: Date) => ({ seconds: BigInt(Math.floor(d.getTime() / 1000)) });

describe("usage headline (in small-daemon-hours)", () => {
  it("reads remaining SDS as small-daemon-hours", () => {
    expect(remainingHeadline(usage({ remainingSmallDaemonSeconds: BigInt(41.5 * 3600) }))).toBe(
      "41.5 small-daemon-hours left",
    );
  });

  // usage_measured=false still renders as UNKNOWN, never "0.0 … left".
  it("renders nothing when the server did not measure", () => {
    expect(remainingHeadline(usage({ usageMeasured: false }))).toBeNull();
    expect(burnLabel(usage({ usageMeasured: false, runningMultiplier: 4n }))).toBeNull();
    expect(runsOutLabel(usage({ usageMeasured: false, exhaustsAt: ts(new Date()) }))).toBeNull();
    expect(asOfLabel(usage({ usageMeasured: false, measuredThrough: ts(new Date()) }))).toBeNull();
    expect(remainingHeadline(undefined)).toBeNull();
  });
});

describe("burn line", () => {
  it("names the size when the period ran exactly one and it is up", () => {
    const u = usage({
      remainingSmallDaemonSeconds: BigInt(41.6 * 3600),
      runningMultiplier: 4n,
      bySize: [{ size: "large", multiplier: 4n, runningSeconds: 100n, overageSeconds: 0n }],
    });
    expect(burnLabel(u)).toBe("≈ 10.4 h left on your Large");
  });

  it("does not guess a size when two are running", () => {
    const u = usage({
      remainingSmallDaemonSeconds: 36000n,
      runningMultiplier: 5n,
      bySize: [
        { size: "small", multiplier: 1n, runningSeconds: 10n, overageSeconds: 0n },
        { size: "large", multiplier: 4n, runningSeconds: 10n, overageSeconds: 0n },
      ],
    });
    expect(burnLabel(u)).toBe("≈ 2.0 h left with your running machines");
  });

  it("is absent when nothing is running", () => {
    expect(burnLabel(usage({ remainingSmallDaemonSeconds: 3600n }))).toBeNull();
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

describe("machine picker facts", () => {
  it("shows concrete runtime, qualified overage, disk and suspended fee", () => {
    expect(sizeFacts(pricing, "large", 160)).toEqual({
      hourlyPriceLabel: "$0.52/h after included hours, with overage enabled",
      includedRuntimeLabel: "40 Large hours/month included",
      diskLabel: "100 GiB disk",
      suspendedFeeLabel: "$25.00/mo",
    });
    for (const [size, runtime] of [["small", 160], ["medium", 80], ["large", 40], ["xl", 20], ["2xl", 10]] as const) {
      expect(sizeFacts(pricing, size, 160)?.includedRuntimeLabel).toMatch(new RegExp(`^${runtime} `));
    }
    expect(sizeFacts(pricing, "small")?.includedRuntimeLabel).toBe("Included runtime unavailable");
    expect(sizeFacts(pricing, "small", 0)?.includedRuntimeLabel).toBe("0 Small hours/month included");
    expect(sizeFacts({ ...pricing, sizes: [{ ...pricing.sizes[0], multiplier: 3 }] }, "small", 150)?.includedRuntimeLabel).toBe("50 Small hours/month included");
    expect(sizeFacts(undefined, "large")).toBeNull();
  });

  it("prices a suspended disk from the server's per-GiB fee", () => {
    expect(suspendedFeeLabel(pricing, 25)).toBe("$6.25/mo");
    expect(suspendedFeeLabel(pricing, 0)).toBeNull();
  });
});

describe("plan tile sentence", () => {
  // FAILS ON main: PlanTiles said "… machine hours are included each month,
  // on every size", which stopped being true once sizes burn at different rates.
  it("no longer says 'on every size' and states each multiplier from the server", () => {
    const sentence = includedHoursSentence(160, pricing, ["small", "medium"]);
    expect(sentence).not.toMatch(/×|burn|small-daemon|Large|XL/);
    expect(sentence).toBe("Each month: 160 Small hours or 80 Medium hours. Shared across your machines, not a separate allowance for each size.");
    expect(includedHoursSentence(undefined, pricing, ["small"])).toContain("unavailable");
  });
});
