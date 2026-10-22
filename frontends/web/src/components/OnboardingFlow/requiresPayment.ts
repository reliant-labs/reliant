/**
 * Does this plan, as chosen, need money before it can run?
 *
 * The single definition, written in the shape of {@link isCloudCompute}:
 * cloud-ness and payment-ness are both properties of a plan computed in
 * exactly ONE place, so no step can disagree with another about them. Two
 * onboarding steps used to answer this question independently — `ComputeStep`
 * via `useCloudEligibility`, `ModelStep` via the wallet balance — and each
 * answered it by ejecting the user to `/settings/billing`. One function and
 * one derived step replace both exits.
 *
 * ── The no-trial reality ──────────────────────────────────────────────
 *
 * `buildPersonalOrg` grants a new account NOTHING. There is no compute free
 * trial and no signup wallet credit; entitlement arrives only from a redeemed
 * coupon or a completed checkout. So the truth table below is not "payment is
 * the exception" — for a new user choosing cloud compute, payment is the
 * NORMAL path, and the only genuinely free way through onboarding is local
 * compute with your own API key.
 *
 * ── Why facts are a parameter ─────────────────────────────────────────
 *
 * Eligibility and wallet balance are server-owned and time-varying. Writing
 * them into the URL-backed plan would put a server fact into user-editable
 * state — the same hazard that made `cloud_paid` route to the local project
 * picker. So they are supplied by the caller and this stays pure over
 * `(plan, facts)`, which is what makes it exhaustively enumerable.
 */
import { isCloudCompute } from "./types";
import type { LaunchPlan } from "./types";

/**
 * The server facts payment depends on.
 *
 * Both are PESSIMISTIC while the underlying queries are in flight — see
 * {@link useOnboardingFacts}. Unknown must read as "might owe", because the
 * failure modes are not symmetric: briefly showing a spinner on the checkout
 * step costs nothing, while briefly skipping it lands an unpaid user on the
 * project steps and then on a first message that fails.
 */
export interface PaymentFacts {
  /** The server agrees this account may run a hosted machine. */
  computeEligible: boolean;
  /** The org wallet has a non-zero balance. */
  walletFunded: boolean;
  /**
   * Reliant sells something in this deployment — `capabilities.billing`.
   *
   * NOT pessimistic, and it is the one fact here that must not be: it is a
   * build constant, so there is no in-flight window to lose a race in. The two
   * above are server reads and stay pessimistic exactly as before.
   */
  reliantBillingAvailable: boolean;
}

export interface PaymentRequirement {
  /** A compute subscription is owed — hosted compute with no entitlement. */
  needsCompute: boolean;
  /** AI credit is owed — Reliant's models against an empty wallet. */
  needsCredit: boolean;
  /** Either. The one field `deriveStep` reads. */
  any: boolean;
}

/**
 * | compute | model            | facts                | owed          |
 * |---------|------------------|----------------------|---------------|
 * | local   | own key          | —                    | nothing       |
 * | local   | reliant_credits  | wallet funded        | nothing       |
 * | local   | reliant_credits  | wallet empty         | credit        |
 * | cloud   | own key          | eligible (coupon/sub)| nothing       |
 * | cloud   | own key          | not eligible         | compute       |
 * | cloud   | reliant_credits  | eligible + funded    | nothing       |
 * | cloud   | reliant_credits  | neither              | compute+credit|
 * | *any*   | *any*            | billing unavailable  | nothing       |
 *
 * The last row is the deployment question, and it dominates the rest: it is
 * asking whether RELIANT is the seller at all, not what the user picked. See
 * the guard at the top of the function.
 *
 * An unset `compute` or `modelProvider` cannot owe anything: the user has not
 * chosen the thing that would cost money yet, and derivation has them on an
 * earlier step regardless.
 *
 * ── Settlement is read per leg, at the same place the debt is computed ──
 *
 * `plan.computeSettled` / `plan.creditSettled` bridge the lag between a
 * webhook landing and the entitlement queries refetching — a confirmed
 * purchase must not derive the user back to a payment they already made.
 *
 * They are read HERE, next to the fact each one stands in for, rather than as
 * a short-circuit wrapped around the whole answer. That is the actual fix for
 * F2: the old single `paid` flag was checked in `checkoutIsOwed`, outside this
 * function and above all per-leg reasoning, so it suppressed the checkout step
 * for a bill it had never paid. A settlement that can only cancel its own leg
 * cannot do that, whatever path the user walked to get here.
 */
export function requiresPayment(
  plan: Partial<LaunchPlan>,
  facts: PaymentFacts,
): PaymentRequirement {
  // Reliant cannot bill for what Reliant does not sell.
  //
  // Both legs below are purchases of OUR products — a machine we host, and
  // model routing we meter. A deployment with no control plane offers neither:
  // `ComputeStep` already hides the cloud card behind `capabilities.cloudDaemons`
  // and every billing RPC throws rather than answering. Charging was never
  // possible; only the ASKING survived, because the pessimistic facts made an
  // unanswerable query read as a debt.
  //
  // This is the deployment half of the owner's rule ("only conditionally do
  // billing if either reliant AI, or reliant compute is chosen") — the plan
  // half is the two conditions below, which already name exactly those two
  // products and nothing else.
  if (!facts.reliantBillingAvailable) {
    return { needsCompute: false, needsCredit: false, any: false };
  }

  const needsCompute =
    isCloudCompute(plan.compute) &&
    !facts.computeEligible &&
    !plan.computeSettled;
  const needsCredit =
    plan.modelProvider === "reliant_credits" &&
    !facts.walletFunded &&
    !plan.creditSettled;
  return { needsCompute, needsCredit, any: needsCompute || needsCredit };
}
