import { describe, expect, it } from "vitest";

import type { Plan, PlanLimits } from "@/gen/controlplane/controlplane/v1/shared_pb";
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
 * There is no intentionally unpriced compute plan, so every unpriced one counts.
 *
 * `plan_compute_free` — a free trial never sold through checkout, with no
 * Stripe price — used to be carved out of this count by its missing
 * `stripe_price_id`. That plan is retired: control-plane no longer lists it,
 * and its catalog test forbids a compute plan without a Stripe price. A
 * carve-out for it would now only hide a genuinely unpriced plan behind an
 * empty `stripe_price_id`.
 */
describe("every unpriced compute plan triggers the stale-catalog alarm", () => {
  it("counts a compute plan with no amount whether or not it names a Stripe price", () => {
    const noStripePrice = { ...computePlan("plan_compute_small", 0n), stripePriceId: "" } as Plan;
    const staleStripePrice = computePlan("plan_compute_medium", 0n); // stripePriceId: "price_test"

    expect(isUnpricedComputePlan(noStripePrice)).toBe(true);
    expect(isUnpricedComputePlan(staleStripePrice)).toBe(true);
  });

  it("does not count a priced plan", () => {
    // Guard the guard: a predicate that always returned true would satisfy
    // the case above.
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
