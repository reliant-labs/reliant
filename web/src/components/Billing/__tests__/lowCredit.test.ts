/**
 * The low-credit threshold, and the states it must not miss.
 *
 * Two failure directions, and they are not symmetric:
 *
 *   - Warning too late leaves a user stuck mid-task with no credit. That is the
 *     failure this feature exists to prevent.
 *   - Warning too early makes the indicator permanent furniture, and an
 *     indicator a healthy user sees every day is one nobody reads — which takes
 *     the genuine warning down with it.
 *
 * So the tests below pin BOTH edges: that a healthy user sees nothing, and that
 * every route to "actually running out" is caught, including the two the runway
 * estimate deliberately refuses to answer.
 */
import { describe, expect, it } from "vitest";

import {
  LOW_CREDIT_RUNWAY_DAYS,
  assessCredit,
  lowCreditLabel,
} from "../lowCredit";

/** Dollars → the wire's nanos. */
const usd = (dollars: number) => BigInt(Math.round(dollars * 1e9));

describe("a user who is fine sees nothing", () => {
  it("stays silent on a healthy balance with a long runway", () => {
    // $100 at $1/day over a 30-day sample = 100 days.
    expect(assessCredit(usd(100), 30, 30).urgency).toBe("healthy");
  });

  it("stays silent just above the threshold", () => {
    // $100 at $10/day = 10 days, comfortably past 7.
    const status = assessCredit(usd(100), 300, 30);
    expect(status.runwayDays).toBe(10);
    expect(status.urgency).toBe("healthy");
  });

  /**
   * The boundary, stated explicitly in both directions. An off-by-one here is
   * invisible in review and changes who gets warned.
   */
  it("fires AT the threshold, not just past it", () => {
    // $70 at $10/day = exactly 7 days.
    const atThreshold = assessCredit(usd(70), 300, 30);
    expect(atThreshold.runwayDays).toBe(LOW_CREDIT_RUNWAY_DAYS);
    expect(atThreshold.urgency).toBe("low");

    // $80 at $10/day = 8 days.
    expect(assessCredit(usd(80), 300, 30).urgency).toBe("healthy");
  });
});

describe("a user who is running out is told, by every route", () => {
  it("warns a heavy spender whose balance still looks large", () => {
    // $50 sounds fine until you know it is $25/day: two days.
    const status = assessCredit(usd(50), 750, 30);
    expect(status.runwayDays).toBe(2);
    expect(status.urgency).toBe("low");
  });

  /**
   * THE GAP THE RUNWAY ALONE LEAVES OPEN, part one.
   *
   * `estimateCreditRunwayDays` returns null under a day, because flooring gives
   * 0 and "0 days" reads as a measurement rather than as "about to stop". That
   * refusal is right for the estimate and catastrophic for the warning: the
   * user closest to running out would be the one told nothing. The balance
   * trigger covers it.
   */
  it("warns on a nearly-empty wallet whose runway rounds below a day", () => {
    // $1 at $10/day is under a day, so the runway withholds.
    const status = assessCredit(usd(1), 300, 30);
    expect(status.runwayDays).toBeNull();
    expect(status.urgency).toBe("low");
  });

  /**
   * THE GAP, part two: a new user with no spend history.
   *
   * The runway needs three days of sample. A user two days in, nearly out of
   * credit, has no rate to project — and is exactly the person most likely to
   * be surprised by running dry.
   */
  it("warns on a low balance with too little history to rate", () => {
    const status = assessCredit(usd(1), 0, 0);
    expect(status.runwayDays).toBeNull();
    expect(status.urgency).toBe("low");
  });

  it("reports empty separately from low — the next request actually fails", () => {
    expect(assessCredit(BigInt(0), 300, 30).urgency).toBe("empty");
    // A negative balance is still empty, not healthy.
    expect(assessCredit(BigInt(-100), 300, 30).urgency).toBe("empty");
  });

  /**
   * A big balance and no measurable spend is the DEFAULT state of a new funded
   * account. It must not warn — this is the case that would otherwise make the
   * indicator permanent for everyone who just topped up.
   */
  it("stays silent on a funded wallet with no spend history", () => {
    const status = assessCredit(usd(50), 0, 0);
    expect(status.runwayDays).toBeNull();
    expect(status.urgency).toBe("healthy");
  });
});

describe("the indicator states a real quantity", () => {
  it("says days, with the unit agreeing with the number", () => {
    expect(lowCreditLabel({ urgency: "low", runwayDays: 3 }, "$12.00")).toBe(
      "~3 days of credit left",
    );
    expect(lowCreditLabel({ urgency: "low", runwayDays: 1 }, "$4.00")).toBe(
      "~1 day of credit left",
    );
  });

  /**
   * When the runway is unknown the balance is the honest second-best: still a
   * real number the user can act on, rather than a fabricated day count.
   */
  it("falls back to the balance rather than inventing a day count", () => {
    expect(lowCreditLabel({ urgency: "low", runwayDays: null }, "$1.20")).toBe(
      "$1.20 credit left",
    );
  });

  it("says nothing quantitative when there is no quantity to state", () => {
    expect(lowCreditLabel({ urgency: "low", runwayDays: null }, null)).toBe(
      "Credit running low",
    );
  });

  it("names the state plainly at zero", () => {
    expect(lowCreditLabel({ urgency: "empty", runwayDays: null }, "$0.00")).toBe(
      "Out of credit",
    );
  });
});
