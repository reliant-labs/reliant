/**
 * Plan and machine size are ONE axis, and the server owns it.
 *
 * `PlanLimits.allowed_daemon_sizes` is the whole rule: a plan unlocks a set of
 * machine sizes, and asking for a size outside that set is what
 * `checkDaemonSizeAllowed` refuses server-side. Two surfaces need to agree
 * about it — the billing purchase grid (which size does this plan buy me?) and
 * the create-machine modal (which sizes may I pick?) — so the rule lives in
 * ONE pure function both of them call, rather than being re-derived twice and
 * drifting.
 *
 * Every fixture here is built from a payload, never from a plan id. A helper
 * that special-cased `plan_compute_medium` would pass a test written against
 * the real catalog and fail the moment a tier was renamed; these use ids the
 * client has never heard of, so id-keyed logic cannot satisfy them.
 */

import { describe, expect, it } from "vitest";

import type { Plan, PlanLimits } from "@/gen/controlplane/v1/public/shared_pb";
import {
  DAEMON_SIZE_ORDER,
  isSizeAllowedByPlan,
  offeredDaemonSizes,
  plansAllowingSize,
  smallestPlanAllowingSize,
} from "../billingUtils";

function plan(
  id: string,
  fields: { priceCents?: bigint; displayOrder?: number } & Partial<PlanLimits>,
): Plan {
  const { priceCents = 0n, displayOrder = 0, ...limits } = fields;
  return {
    id,
    productId: "prod_compute",
    name: id,
    tier: 0,
    stripePriceId: "price_test",
    limits: "",
    priceCents,
    displayOrder,
    structuredLimits: {
      maxSeats: 1,
      maxWorkspaces: 1,
      maxLlmSpendMonthly: 0,
      maxLlmKeys: 1,
      requestsPerMin: 0,
      allowedDaemonSizes: [],
      daemonComputeIncludedMinutes: 0,
      daemonOveragePerMinuteCents: 0,
      ...limits,
    },
  } as unknown as Plan;
}

/** A catalog with ids no client hardcodes, so id-keyed logic cannot pass. */
const catalog = [
  plan("tier_alpha", {
    priceCents: 2000n,
    displayOrder: 1,
    allowedDaemonSizes: ["small"],
  }),
  plan("tier_beta", {
    priceCents: 4000n,
    displayOrder: 2,
    allowedDaemonSizes: ["small", "medium"],
  }),
  plan("tier_gamma", {
    priceCents: 8000n,
    displayOrder: 3,
    allowedDaemonSizes: ["small", "medium", "large"],
  }),
];

describe("isSizeAllowedByPlan", () => {
  it("allows a size the plan's limits name", () => {
    expect(isSizeAllowedByPlan(catalog[1], "medium")).toBe(true);
    expect(isSizeAllowedByPlan(catalog[1], "small")).toBe(true);
  });

  it("refuses a size the plan's limits omit", () => {
    expect(isSizeAllowedByPlan(catalog[1], "large")).toBe(false);
    expect(isSizeAllowedByPlan(catalog[1], "xl")).toBe(false);
  });

  it("matches case-insensitively, because the catalog stores lowercase names", () => {
    expect(isSizeAllowedByPlan(catalog[2], "LARGE" as "large")).toBe(true);
  });

  /**
   * Fail CLOSED. An absent plan, or one whose limits never reached the wire,
   * must not silently permit every size — that is exactly the regression that
   * made the size picker render empty while claiming a plan was active.
   */
  it("refuses every size when there is no plan", () => {
    expect(isSizeAllowedByPlan(null, "small")).toBe(false);
    expect(isSizeAllowedByPlan(undefined, "small")).toBe(false);
  });

  it("refuses every size when the plan names none", () => {
    const bare = plan("tier_bare", { priceCents: 1000n });
    for (const size of DAEMON_SIZE_ORDER) {
      expect(isSizeAllowedByPlan(bare, size)).toBe(false);
    }
  });
});

describe("plansAllowingSize", () => {
  it("keeps only the plans whose limits include the size", () => {
    expect(plansAllowingSize(catalog, "large").map((p) => p.id)).toEqual([
      "tier_gamma",
    ]);
    expect(plansAllowingSize(catalog, "medium").map((p) => p.id)).toEqual([
      "tier_beta",
      "tier_gamma",
    ]);
  });

  it("returns nothing for a size no plan in the catalog offers", () => {
    expect(plansAllowingSize(catalog, "xl")).toEqual([]);
  });
});

describe("smallestPlanAllowingSize", () => {
  /**
   * "Cheapest that can run this" is the question a user actually asks, and it
   * has to be answered from display_order + price rather than from a client's
   * idea of which tier is which.
   */
  it("picks the lowest-ordered plan that allows the size", () => {
    expect(smallestPlanAllowingSize(catalog, "small")?.id).toBe("tier_alpha");
    expect(smallestPlanAllowingSize(catalog, "medium")?.id).toBe("tier_beta");
    expect(smallestPlanAllowingSize(catalog, "large")?.id).toBe("tier_gamma");
  });

  it("is undefined when nothing in the catalog runs that size", () => {
    expect(smallestPlanAllowingSize(catalog, "xl")).toBeUndefined();
  });

  it("ignores catalog order and uses display_order", () => {
    const shuffled = [catalog[2], catalog[0], catalog[1]];
    expect(smallestPlanAllowingSize(shuffled, "small")?.id).toBe("tier_alpha");
  });
});

describe("offeredDaemonSizes", () => {
  /**
   * Which sizes the picker shows is a server fact — the union of what the
   * catalog's plans allow — not the client's four-entry enum. A catalog that
   * stops selling XL must stop offering XL with no frontend change.
   */
  it("is the union of every plan's allowed sizes, in size order", () => {
    expect(offeredDaemonSizes(catalog)).toEqual(["small", "medium", "large"]);
  });

  it("includes a size only one plan offers", () => {
    const withXl = [
      ...catalog,
      plan("tier_delta", {
        priceCents: 16000n,
        displayOrder: 4,
        allowedDaemonSizes: ["small", "medium", "large", "xl"],
      }),
    ];
    expect(offeredDaemonSizes(withXl)).toEqual([
      "small",
      "medium",
      "large",
      "xl",
    ]);
  });

  it("is empty when the catalog names no sizes at all", () => {
    expect(offeredDaemonSizes([plan("tier_bare", { priceCents: 1000n })])).toEqual(
      [],
    );
  });

  it("drops a size name the client does not understand rather than rendering it", () => {
    const odd = [
      plan("tier_odd", { priceCents: 1000n, allowedDaemonSizes: ["small", "titan"] }),
    ];
    expect(offeredDaemonSizes(odd)).toEqual(["small"]);
  });
});
