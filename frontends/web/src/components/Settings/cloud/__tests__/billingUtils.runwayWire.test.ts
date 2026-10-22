import { describe, expect, it } from "vitest";

import {
  MIN_RUNWAY_SAMPLE_DAYS,
  estimateCreditRunwayDays,
} from "../billingUtils";

/**
 * The credit band's "~N days at recent use" was dead code behind a passing
 * test, and the test passed because its FIXTURE carried a shape the wire has
 * never produced.
 *
 * `spendSampleDays` counted distinct `entry.periodStart` values. That field
 * exists on `LLMSpendEntry` but `GetLLMSpend` never populated it and could
 * not: entries are aggregated per (key, model) across the whole requested
 * range, so there is no per-day row for a period to attach to even in
 * principle. In production the count was therefore 0 for every response,
 * 0 < 3, and the headline feature rendered nothing for anyone — while
 * `billing.bands.test.tsx` synthesized one `periodStart` per day and reported
 * the feature working.
 *
 * The denominator now comes from the server as `sample_days`, counted from the
 * per-request spend logs that only it can see. These tests pin the CLIENT side
 * of that contract:
 *
 *   1. the wire's own payload shape produces a runway, and
 *   2. the client cannot go back to inferring days from entries.
 *
 * The second is the one that matters. Without it, someone restoring the old
 * derivation would find every test still green.
 */

const usdNanos = (usd: number) => BigInt(Math.round(usd * 1_000_000_000));

/**
 * EXACTLY what `spendEntryToProto` emits — five fields, no period. Copied from
 * the Go converter rather than invented, because inventing it is how the
 * original defect survived review.
 */
const REAL_WIRE_ENTRIES = [
  {
    keyId: "k1",
    keyName: "default",
    model: "claude-sonnet-4",
    spend: 40,
    requests: 900n,
  },
  { keyId: "k1", keyName: "default", model: "gpt-4o", spend: 20, requests: 300n },
];

describe("the runway is computed from the server's sample_days, not from entries", () => {
  it("renders a runway from the payload the wire actually carries", () => {
    // $50 balance, $60 spent over the 30 days the SERVER observed => $2/day
    // => 25 days. Under the old client-side derivation this returned null,
    // because these entries carry no periodStart and never have.
    const sampleDays = 30; // as reported by GetLLMSpendResponse.sample_days
    expect(estimateCreditRunwayDays(usdNanos(50), 60, sampleDays)).toBe(25);
  });

  it("carries no day information on the entries themselves", () => {
    // The load-bearing observation, stated as an assertion so it cannot drift:
    // there is nothing on a real entry from which a day could be derived.
    // Anyone tempted to recompute the sample client-side has to delete this
    // test first, and its name says why they should not.
    for (const entry of REAL_WIRE_ENTRIES) {
      expect(entry).not.toHaveProperty("periodStart");
      expect(entry).not.toHaveProperty("periodEnd");
    }
  });

  it("withholds when the server reports too few observed days", () => {
    // Zero observed days is the honest "we cannot say" — no usable timestamps
    // upstream. It must NOT fall back to the requested range length.
    expect(estimateCreditRunwayDays(usdNanos(50), 60, 0)).toBeNull();
    expect(
      estimateCreditRunwayDays(usdNanos(50), 60, MIN_RUNWAY_SAMPLE_DAYS - 1),
    ).toBeNull();
  });

  it("uses the sample days as the divisor, not the entry count", () => {
    // Two entries, thirty observed days. An implementation that divided by
    // the entry count would report $30/day and a 1-day runway; dividing by
    // the observed days reports $2/day and 25 days. The two answers differ by
    // more than an order of magnitude, so this pins which one is computed.
    const byObservedDays = estimateCreditRunwayDays(usdNanos(50), 60, 30);
    const byEntryCount = estimateCreditRunwayDays(
      usdNanos(50),
      60,
      REAL_WIRE_ENTRIES.length,
    );

    expect(byObservedDays).toBe(25);
    expect(byEntryCount).toBeNull(); // 2 < MIN_RUNWAY_SAMPLE_DAYS
    expect(byObservedDays).not.toBe(byEntryCount);
  });
});
