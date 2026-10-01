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
    expect(burnLabel(u)).toBe("≈ 10.4 h on your Large (4×)");
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
    expect(burnLabel(u)).toBe("≈ 2.0 h at the current rate (5×)");
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
  it("shows price, burn rate, disk and the suspended fee for a size", () => {
    expect(sizeFacts(pricing, "large")).toEqual({
      hourlyPriceLabel: "$0.52/h past your included hours",
      burnRateLabel: "uses included hours 4× as fast",
      diskLabel: "100 GiB disk",
      suspendedFeeLabel: "$25.00/mo",
    });
    expect(sizeFacts(pricing, "small")?.burnRateLabel).toBe("uses included hours 1× (1 hour per hour)");
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
    const sentence = includedHoursSentence(320, pricing);
    expect(sentence).not.toMatch(/on every size/);
    expect(sentence).toBe(
      "320 small-daemon-hours each month. Medium uses them 2× as fast, Large 4×, XL 8×, 2XL 16×. " +
        "Suspended machines: $0.25 per GiB-month.",
    );
  });
});
