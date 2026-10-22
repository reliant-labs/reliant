/**
 * When to tell someone their credit is running out, and how loudly.
 *
 * The arithmetic this rests on already exists and is already tested:
 * `estimateCreditRunwayDays` turns a balance and an observed burn rate into
 * days remaining, and withholds rather than guessing when the sample is too
 * short. This file adds only the DECISION — at what point a number worth
 * knowing becomes a number worth interrupting for — and keeps it as pure
 * functions so the threshold is testable without mounting a component.
 *
 * ── The threshold: 7 days ─────────────────────────────────────────────
 *
 * The number has to clear two bars. Too low and the warning arrives after the
 * user is already stuck — the thing it exists to prevent. Too high and it is
 * permanent furniture: an indicator a healthy user sees every day is one they
 * stop seeing, and it takes the genuine warning down with it.
 *
 * Seven days is the smallest window that still permits an UNHURRIED response.
 * Topping up is not instant for everyone: a card can decline, a company card
 * may not be to hand, an invoice may need approval, and a weekend can sit in
 * the middle of any of it. Three days does not survive a Friday afternoon.
 * Fourteen would be safer still, but a fortnight of warning about a balance
 * that has not moved is how an indicator becomes wallpaper.
 *
 * It is expressed in DAYS rather than dollars because dollars are meaningless
 * without a burn rate: $10 is a fortnight for one user and an afternoon for
 * another, and a fixed dollar threshold would nag the first while failing the
 * second entirely.
 *
 * ── The floor, and why a dollar threshold still exists ────────────────
 *
 * `estimateCreditRunwayDays` returns null for a runway under one day — flooring
 * gives 0, which reads as a measurement rather than as "about to stop" — and
 * null for a spend sample under three days. Both are correct refusals for a
 * runway ESTIMATE and both would silently suppress the warning at exactly the
 * moment it matters most: a nearly-empty wallet, and a brand-new user who has
 * not been spending long enough to have a rate.
 *
 * So the balance itself is a second, independent trigger. `getWalletBalanceState`
 * already draws that line at $2.50, and reusing it keeps the ambient indicator
 * and the existing settings warning from disagreeing about what "low" means.
 *
 * ── Zero is a different thing and gets a different surface ────────────
 *
 * Everything above is ambient: a persistent, ignorable indicator. At a zero
 * balance the next request actually fails, and that is the ONE moment an
 * interruption is justified — see `LowCreditModal`.
 */

import {
  estimateCreditRunwayDays,
  getWalletBalanceState,
} from "@/components/Settings/cloud/billingUtils";

/**
 * Days of runway at or below which the ambient indicator appears.
 *
 * See the header for why seven. Exported because the modal's copy quotes it and
 * the tests pin it — a threshold restated in three places is a threshold that
 * will eventually disagree with itself.
 */
export const LOW_CREDIT_RUNWAY_DAYS = 7;

export type CreditUrgency =
  /** Nothing to say. The indicator does not render at all. */
  | "healthy"
  /** Ambient indicator: a real quantity, click-through to top-up. */
  | "low"
  /** Zero balance — the next request fails. Modal, once. */
  | "empty";

export interface CreditStatus {
  urgency: CreditUrgency;
  /**
   * Whole days of credit left, or null when we cannot honestly say.
   *
   * Null with `urgency: "low"` is a real combination — a nearly-empty wallet
   * with too little spend history to rate — and callers must render the
   * balance instead of inventing a day count.
   */
  runwayDays: number | null;
}

/**
 * Grade a wallet's balance and burn rate into what the UI should do about it.
 *
 * `spendUsd` and `sampleDays` come from the server's 30-day spend window;
 * `sampleDays: 0` is the honest "we cannot say" and suppresses the runway
 * without suppressing the balance-based trigger.
 */
export function assessCredit(
  balanceNanos: bigint,
  spendUsd: number,
  sampleDays: number,
): CreditStatus {
  const runwayDays = estimateCreditRunwayDays(balanceNanos, spendUsd, sampleDays);
  const balanceState = getWalletBalanceState(balanceNanos);

  // Zero first: it is the only state where the next request actually fails, and
  // it must not be reachable through any softer branch below.
  if (balanceState === "empty") {
    return { urgency: "empty", runwayDays: null };
  }

  // Either trigger fires independently. The runway catches a heavy user with a
  // healthy-looking balance; the balance catches a light user whose runway is
  // unmeasurable or rounds below a day.
  const runwayIsLow = runwayDays !== null && runwayDays <= LOW_CREDIT_RUNWAY_DAYS;
  if (runwayIsLow || balanceState === "low") {
    return { urgency: "low", runwayDays };
  }

  return { urgency: "healthy", runwayDays };
}

/**
 * What the ambient indicator says.
 *
 * Always a REAL QUANTITY — "~3 days of credit left", not "low balance" — because
 * the quantity is the whole reason to surface this: it tells the user whether to
 * act now or after lunch, which "low" never does.
 *
 * Falls back to the balance when the runway is unknown. That is the honest
 * second-best: it is still a real number the user can act on, unlike a
 * fabricated day count.
 */
export function lowCreditLabel(
  status: CreditStatus,
  formattedBalance: string | null,
): string {
  if (status.urgency === "empty") return "Out of credit";
  if (status.runwayDays !== null) {
    const unit = status.runwayDays === 1 ? "day" : "days";
    return `~${status.runwayDays} ${unit} of credit left`;
  }
  return formattedBalance
    ? `${formattedBalance} credit left`
    : "Credit running low";
}
