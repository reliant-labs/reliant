import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import type { Plan, PlanLimits } from "@/gen/controlplane/v1/public/shared_pb";
import {
  MIN_RUNWAY_SAMPLE_DAYS,
  deriveComputeCapacity,
  estimateCreditRunwayDays,
  formatRunway,
  isPlanDetailUnavailable,
} from "../billingUtils";

/**
 * The two Overview bands read DIFFERENT facts about their product, and this
 * file pins the facts rather than the pixels.
 *
 * Credit is a meter that drains: balance plus a runway estimate. Compute is a
 * capacity with a ceiling: hours used against hours included, and where the
 * boundary falls. Neither of those derivations existed while the page rendered
 * the wallet's shape twice, which is why the two products looked identical.
 *
 * The runway is an ESTIMATE ABOUT MONEY, so the rules that suppress it matter
 * more than the arithmetic: too small a sample, no spend, or a balance that
 * does not last a day all render nothing at all rather than a number a user
 * would quote back at us.
 */

const NANOS_PER_USD = 1_000_000_000;
const usdNanos = (usd: number) => BigInt(Math.round(usd * NANOS_PER_USD));

// ── Runway ────────────────────────────────────────────────────────────

describe("credit runway is estimated, or withheld — never guessed", () => {
  it("divides the balance by observed daily burn", () => {
    // $18.40 balance, $6.00 spent over 3 days = $2/day → 9 days.
    expect(estimateCreditRunwayDays(usdNanos(18.4), 6, 3)).toBe(9);
  });

  /**
   * The load-bearing one. A fixture whose balance and spend happen to divide
   * into the same answer at two different sample widths cannot tell a real
   * per-day rate from a hardcoded window. This one changes ONLY the sample
   * width, so an implementation that assumed a fixed 30-day divisor fails.
   */
  it("tracks the sample width rather than assuming a fixed window", () => {
    const balance = usdNanos(60);
    expect(estimateCreditRunwayDays(balance, 30, 30)).toBe(60); // $1/day
    expect(estimateCreditRunwayDays(balance, 30, 10)).toBe(20); // $3/day
    expect(estimateCreditRunwayDays(balance, 30, 6)).toBe(12); // $5/day
  });

  it("withholds an estimate below the minimum sample", () => {
    // Two days of spend produces a number that swings wildly day to day.
    expect(
      estimateCreditRunwayDays(usdNanos(100), 10, MIN_RUNWAY_SAMPLE_DAYS - 1),
    ).toBeNull();
    // And the first day the sample is wide enough, it starts answering — so
    // this is a threshold, not a blanket refusal.
    expect(
      estimateCreditRunwayDays(usdNanos(100), 10, MIN_RUNWAY_SAMPLE_DAYS),
    ).not.toBeNull();
  });

  it("withholds an estimate when there is no spend to extrapolate from", () => {
    expect(estimateCreditRunwayDays(usdNanos(100), 0, 30)).toBeNull();
  });

  it("withholds an estimate on an empty or negative balance", () => {
    // The empty-wallet warning already speaks here; "~0 days" beside it is
    // noise, and a negative balance has no runway to report at all.
    expect(estimateCreditRunwayDays(BigInt(0), 30, 30)).toBeNull();
    expect(estimateCreditRunwayDays(usdNanos(-5), 30, 30)).toBeNull();
  });

  it("withholds an estimate when the balance does not last a day", () => {
    // $0.50 against $30/day. Flooring gives 0, and "~0 days" reads as a
    // measured value rather than as "about to run out".
    expect(estimateCreditRunwayDays(usdNanos(0.5), 30, 30)).toBeNull();
  });

  it("hedges the label and never states a bare number of days", () => {
    expect(formatRunway(9)).toBe("~9 days at recent use");
    expect(formatRunway(1)).toBe("~1 day at recent use");
  });

  it("stops counting past the point the estimate means anything", () => {
    // A 4,000-day runway is arithmetic, not information, and quoting it
    // invites someone to hold us to it.
    expect(formatRunway(4000)).toBe("~90+ days at recent use");
    expect(formatRunway(90)).toBe("~90+ days at recent use");
    expect(formatRunway(89)).toBe("~89 days at recent use");
  });
});

describe("the spend sample comes from the server, not from the entries", () => {
  /**
   * `spendSampleDays` used to live in billingUtils and count distinct
   * `entry.periodStart` values. GetLLMSpend has never populated that field
   * and structurally cannot — entries are aggregated per (key, model) across
   * the whole range — so it returned 0 for every real response and the runway
   * silently never rendered. The tests that used to sit here passed because
   * they fed it a shape only a fixture ever produced.
   *
   * The count now arrives as GetLLMSpendResponse.sample_days. This assertion
   * is STRUCTURAL for the same reason billingUtils.price.test.ts reads the
   * source: a behavioural test cannot fail if someone reintroduces a
   * client-side derivation, because the derivation would look correct against
   * any fixture that carries the invented field.
   */
  it("exports no client-side day-counting helper", async () => {
    const utils = await import("../billingUtils");
    expect("spendSampleDays" in utils).toBe(false);
  });

  it("does not read periodStart anywhere in billingUtils", () => {
    const source = readFileSync(
      resolve(__dirname, "../billingUtils.ts"),
      "utf8",
    );
    // Comments explaining the removal are fine; code that reads the field is
    // not. Strip line comments before checking.
    const code = source
      .split("\n")
      .filter((line) => !line.trim().startsWith("//") && !line.trim().startsWith("*"))
      .join("\n");
    expect(code).not.toMatch(/periodStart/);
  });
});

// ── Compute capacity ──────────────────────────────────────────────────

describe("compute reads as a capacity with a ceiling", () => {
  const capacity = (usedMinutes: number, includedMinutes: number, overageMinutes = 0) =>
    deriveComputeCapacity({ usedMinutes, includedMinutes, overageMinutes });

  it("reports how much of the included allowance is consumed", () => {
    const c = capacity(720, 2400); // 12 h of 40 h
    expect(c.usedPct).toBeCloseTo(30);
    expect(c.state).toBe("under");
  });

  it("warns before the ceiling rather than at it", () => {
    // 80% is the point at which the bar changes state. The pair matters: a
    // predicate stuck on either side of the boundary satisfies one of these
    // and fails the other.
    expect(capacity(1919, 2400).state).toBe("under");
    expect(capacity(1920, 2400).state).toBe("near");
  });

  it("distinguishes a spent allowance from one running into overage", () => {
    // Both are "past the line", and they are not the same thing: one stops
    // machines starting, the other is being charged for.
    expect(capacity(2400, 2400).state).toBe("spent");
    expect(capacity(2500, 2400, 100).state).toBe("overage");
  });

  it("never lets the included fill exceed the ceiling it is drawn against", () => {
    // The overage is a SEPARATE segment past the boundary; letting used
    // minutes overflow the included bar erases the boundary the band exists
    // to show.
    const c = capacity(3000, 2400, 600);
    expect(c.usedPct).toBe(100);
    expect(c.overagePct).toBeGreaterThan(0);
  });

  it("treats the unlimited sentinel as unlimited, not as a zero ceiling", () => {
    // -1 is the catalog's "unlimited". Dividing by it produces a negative
    // percentage and a bar that renders as though the user had overrun.
    const c = capacity(5000, -1);
    expect(c.state).toBe("unlimited");
    expect(c.usedPct).toBe(0);
    expect(c.overagePct).toBe(0);
  });
});

// ── Degraded plan detail ──────────────────────────────────────────────

function plan(limits: Partial<PlanLimits>): Plan {
  return {
    id: "tier_x",
    productId: "prod_compute",
    name: "Tier X",
    priceCents: 4000n,
    displayOrder: 1,
    structuredLimits: {
      allowedDaemonSizes: [],
      daemonComputeIncludedMinutes: 0,
      daemonOveragePerMinuteCents: 0,
      ...limits,
    },
  } as unknown as Plan;
}

/**
 * The screenshot's `— / — / 0 h` state. The data ARRIVED and was unusable, and
 * it rendered at full confidence next to a purchase button. This predicate
 * states the stale-row signature once so every render site reads the same rule
 * instead of re-deriving it.
 */
describe("a plan whose detail never loaded is distinguishable from a real one", () => {
  it("flags the stale signature: no sizes, no minutes, no rate", () => {
    expect(
      isPlanDetailUnavailable(
        plan({
          allowedDaemonSizes: [],
          daemonComputeIncludedMinutes: 0,
          daemonOveragePerMinuteCents: 0,
        }),
      ),
    ).toBe(true);
  });

  /**
   * The mutation guard. A predicate that merely checked "included minutes is
   * zero" would flag this plan too — and this is a legitimate plan that
   * genuinely includes no hours, sold on pure overage. Getting it wrong
   * disables the overage control for a user whose rate is right there.
   */
  it("does not flag a real plan that includes zero hours but has sizes and a rate", () => {
    expect(
      isPlanDetailUnavailable(
        plan({
          allowedDaemonSizes: ["small"],
          daemonComputeIncludedMinutes: 0,
          daemonOveragePerMinuteCents: 0.4,
        }),
      ),
    ).toBe(false);
  });

  it("does not flag a plan carrying any one of the three facts", () => {
    // All three absent TOGETHER is the signature. Any single one present
    // means the row was populated, so the reading is a real plan.
    expect(
      isPlanDetailUnavailable(plan({ allowedDaemonSizes: ["medium"] })),
    ).toBe(false);
    expect(
      isPlanDetailUnavailable(plan({ daemonComputeIncludedMinutes: 2400 })),
    ).toBe(false);
    expect(
      isPlanDetailUnavailable(plan({ daemonOveragePerMinuteCents: 0.4 })),
    ).toBe(false);
  });

  it("does not flag an unlimited plan", () => {
    // -1 included minutes with no sizes listed is still a populated row.
    expect(
      isPlanDetailUnavailable(plan({ daemonComputeIncludedMinutes: -1 })),
    ).toBe(false);
  });

  /**
   * No subscription at all is the TRUE empty, and it gets a purposeful empty
   * state ("pick a plan"), not the "control plane may not have restarted"
   * advice — which would send a brand new user to look at infrastructure.
   */
  it("reports absence of a plan as not-degraded, because that is a different state", () => {
    expect(isPlanDetailUnavailable(null)).toBe(false);
    expect(isPlanDetailUnavailable(undefined)).toBe(false);
  });
});
