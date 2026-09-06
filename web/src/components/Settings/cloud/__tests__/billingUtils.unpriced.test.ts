import { describe, expect, it } from "vitest";

import type { Plan, PlanLimits } from "@/gen/controlplane/v1/public/shared_pb";
import {
  isComputePlan,
  isPurchasableComputePlan,
  isUnpricedComputePlan,
} from "../billingUtils";

/**
 * "This environment sells nothing" and "the plans arrived without prices" are
 * different facts, and the billing page must be able to tell them apart.
 *
 * The real incident: `isPurchasableComputePlan` requires `priceCents > 0`, and
 * price reaches the client from the `plans` table — which `plansync` rewrites
 * from the catalog AT STARTUP. A control plane still running against rows
 * written before the catalog gained `price_cents` therefore serves four fully
 * populated compute plans with no price. Every one is filtered out of the
 * grid, and the page announced "Compute plans are not configured for this
 * environment yet" for an environment whose catalog is entirely populated.
 * Confirmed against the dev database: the rows carried `allowed_daemon_sizes`
 * and `daemon_compute_included_minutes` but no `price_cents`.
 *
 * That message sends someone to look at config that is already correct. The
 * distinguishing signal was there the whole time — the plans arrived, they
 * just could not be priced — so these two predicates keep it available to the
 * caller instead of collapsing it into one empty array.
 */

function computePlan(id: string, priceCents: bigint): Plan {
  return {
    id,
    productId: "prod_compute",
    name: id,
    tier: 0,
    stripePriceId: "price_test",
    limits: "",
    priceCents,
    displayOrder: 0,
    structuredLimits: {
      allowedDaemonSizes: ["small"],
      daemonComputeIncludedMinutes: 1000,
    } as PlanLimits,
  } as Plan;
}

/** A non-compute plan, to prove the product filter is doing real work. */
function codePlan(id: string): Plan {
  return { ...computePlan(id, 0n), productId: "prod_code" } as Plan;
}

/**
 * A plan the catalog INTENDS to be unpriced is not a symptom.
 *
 * `plan_compute_free` ships with `stripe_price_id: null` and no `price_cents`
 * because a free trial is never charged through Stripe checkout. It is still
 * `productId: prod_compute`, so it was counted as an unpriced compute plan —
 * and in an environment seeded with only the free plan the page told the user
 * "Plan pricing unavailable … Restart the control plane" about a catalog that
 * was entirely correct.
 *
 * That is the alarming-message defect this count was added to eliminate,
 * firing on the healthy case. The distinguishing signal is on the wire
 * already: a plan MEANT to be sold carries a `stripe_price_id`, so a plan with
 * no Stripe price and no amount is deliberate, while a plan WITH a Stripe
 * price but no amount is the stale-catalog symptom the message describes.
 */
function freeTrialPlan(): Plan {
  return {
    ...computePlan("plan_compute_free", 0n),
    // Exactly the catalog's shape: no Stripe price at all.
    stripePriceId: "",
  } as Plan;
}

describe("intentionally unpriced plans do not trigger the stale-catalog alarm", () => {
  it("does not count the free trial as an unpriced plan", () => {
    const served = [freeTrialPlan()];

    const unpriced = served.filter(isUnpricedComputePlan);

    expect(unpriced).toHaveLength(0);
  });

  it("still counts a plan that HAS a Stripe price but arrived with no amount", () => {
    // The real symptom: plansync wrote the row before the catalog gained
    // price_cents, so Stripe knows the price and our row does not. Losing
    // this case would trade one false message for a silent one.
    const stale = computePlan("plan_compute_small", 0n); // stripePriceId: "price_test"

    expect(isUnpricedComputePlan(stale)).toBe(true);
  });

  it("does not count the free trial even alongside genuinely stale plans", () => {
    const served = [
      freeTrialPlan(),
      computePlan("plan_compute_small", 0n),
      computePlan("plan_compute_medium", 0n),
    ];

    expect(served.filter(isUnpricedComputePlan)).toHaveLength(2);
  });

  it("does not count a priced plan", () => {
    // Guard the guard: a predicate that always returned false would satisfy
    // the two zero-expecting cases above.
    expect(isUnpricedComputePlan(computePlan("plan_compute_small", 2000n))).toBe(
      false,
    );
  });
});

describe("unpriced compute plans are distinguishable from an empty catalog", () => {
  it("treats a compute plan with no price as present-but-unpurchasable", () => {
    // The exact shape the stale dev database served.
    const stale = computePlan("plan_compute_small", 0n);

    expect(isComputePlan(stale)).toBe(true);
    expect(isPurchasableComputePlan(stale)).toBe(false);
  });

  it("counts unpriced compute plans without counting other products", () => {
    // The page uses this count to choose between two very different messages,
    // so a non-compute plan leaking in would report a compute-catalog problem
    // that does not exist.
    const served = [
      computePlan("plan_compute_small", 0n),
      computePlan("plan_compute_medium", 0n),
      codePlan("plan_code_pro"),
    ];

    const unpriced = served.filter(
      (p) => isComputePlan(p) && !isPurchasableComputePlan(p),
    );

    expect(unpriced).toHaveLength(2);
  });

  it("reports zero unpriced plans when the catalog is genuinely empty", () => {
    // The other real state: nothing to sell here. It must NOT produce the
    // "restart the control plane" advice, which would be a wild goose chase.
    const served: Plan[] = [];

    const unpriced = served.filter(
      (p) => isComputePlan(p) && !isPurchasableComputePlan(p),
    );

    expect(unpriced).toHaveLength(0);
  });

  it("reports zero unpriced plans when every plan is priced", () => {
    // Guard-the-guard: without this, a broken `isComputePlan` that always
    // returned false would satisfy the two zero-expecting cases above and the
    // suite would still look green.
    const served = [
      computePlan("plan_compute_small", 2000n),
      computePlan("plan_compute_medium", 4000n),
    ];

    const unpriced = served.filter(
      (p) => isComputePlan(p) && !isPurchasableComputePlan(p),
    );

    expect(unpriced).toHaveLength(0);
    expect(served.filter(isComputePlan)).toHaveLength(2);
  });
});
