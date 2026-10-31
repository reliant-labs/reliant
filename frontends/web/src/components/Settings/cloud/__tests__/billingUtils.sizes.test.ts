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
 * that special-cased `plan_compute_l` would pass a test written against
 * the real catalog and fail the moment a tier was renamed; these use ids the
 * client has never heard of, so id-keyed logic cannot satisfy them.
 */

import { describe, expect, it } from "vitest";

import type { Plan, PlanLimits } from "@/gen/controlplane/controlplane/v1/shared_pb";
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
      includedSmallDaemonHours: 0n,
      ...limits,
    },
  } as unknown as Plan;
}

/** A catalog with ids no client hardcodes, so id-keyed logic cannot pass. */
const catalog = [
  plan("tier_alpha", {
    priceCents: 2000n,
    displayOrder: 1,
    allowedDaemonSizes: ["m"],
  }),
  plan("tier_beta", {
    priceCents: 4000n,
    displayOrder: 2,
    allowedDaemonSizes: ["m", "l"],
  }),
  plan("tier_gamma", {
    priceCents: 8000n,
    displayOrder: 3,
    allowedDaemonSizes: ["m", "l", "xl"],
  }),
];

describe("isSizeAllowedByPlan", () => {
  it("allows a size the plan's limits name", () => {
    expect(isSizeAllowedByPlan(catalog[1], "l")).toBe(true);
    expect(isSizeAllowedByPlan(catalog[1], "m")).toBe(true);
  });

  it("refuses a size the plan's limits omit", () => {
    expect(isSizeAllowedByPlan(catalog[1], "xl")).toBe(false);
    expect(isSizeAllowedByPlan(catalog[1], "2xl")).toBe(false);
  });

  it("matches case-insensitively, because the catalog stores lowercase names", () => {
    expect(isSizeAllowedByPlan(catalog[2], "XL" as "xl")).toBe(true);
  });

  /**
   * Fail CLOSED. An absent plan, or one whose limits never reached the wire,
   * must not silently permit every size — that is exactly the regression that
   * made the size picker render empty while claiming a plan was active.
   */
  it("refuses every size when there is no plan", () => {
    expect(isSizeAllowedByPlan(null, "m")).toBe(false);
    expect(isSizeAllowedByPlan(undefined, "m")).toBe(false);
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
    expect(plansAllowingSize(catalog, "xl").map((p) => p.id)).toEqual([
      "tier_gamma",
    ]);
    expect(plansAllowingSize(catalog, "l").map((p) => p.id)).toEqual([
      "tier_beta",
      "tier_gamma",
    ]);
  });

  it("returns nothing for a size no plan in the catalog offers", () => {
    expect(plansAllowingSize(catalog, "2xl")).toEqual([]);
  });
});

describe("smallestPlanAllowingSize", () => {
  /**
   * "Cheapest that can run this" is the question a user actually asks, and it
   * has to be answered from display_order + price rather than from a client's
   * idea of which tier is which.
   */
  it("picks the lowest-ordered plan that allows the size", () => {
    expect(smallestPlanAllowingSize(catalog, "m")?.id).toBe("tier_alpha");
    expect(smallestPlanAllowingSize(catalog, "l")?.id).toBe("tier_beta");
    expect(smallestPlanAllowingSize(catalog, "xl")?.id).toBe("tier_gamma");
  });

  it("is undefined when nothing in the catalog runs that size", () => {
    expect(smallestPlanAllowingSize(catalog, "2xl")).toBeUndefined();
  });

  it("ignores catalog order and uses display_order", () => {
    const shuffled = [catalog[2], catalog[0], catalog[1]];
    expect(smallestPlanAllowingSize(shuffled, "m")?.id).toBe("tier_alpha");
  });
});

describe("offeredDaemonSizes", () => {
  /**
   * Which sizes the picker shows is a server fact — the union of what the
   * catalog's plans allow — not the client's four-entry enum. A catalog that
   * stops selling XL must stop offering XL with no frontend change.
   */
  it("is the union of every plan's allowed sizes, in size order", () => {
    expect(offeredDaemonSizes(catalog)).toEqual(["m", "l", "xl"]);
  });

  it("includes a size only one plan offers", () => {
    const withXl = [
      ...catalog,
      plan("tier_delta", {
        priceCents: 16000n,
        displayOrder: 4,
        allowedDaemonSizes: ["m", "l", "xl", "2xl"],
      }),
    ];
    expect(offeredDaemonSizes(withXl)).toEqual([
      "m",
      "l",
      "xl",
      "2xl",
    ]);
  });

  /**
   * 2xl is a real, priced tier and must be offerable.
   *
   * `plan_compute_3xl` sells at $299 with a Stripe price id, so a client that
   * cannot label the size does not omit the tier gracefully — this function
   * DROPS any size `DAEMON_SIZE_ORDER` does not list, which made the top of
   * the ladder silently unbuyable with nothing on screen to explain it. Same
   * class of defect as the hardcoded plan allowlist billingUtils deleted: a
   * client-side table deciding which server products exist.
   *
   * The catalog still decides whether a 2xl row APPEARS — the assertion below
   * about a catalog that sells nothing bigger than large is what pins that.
   */
  it("offers 2xl when the catalog sells it", () => {
    const with2xl = [
      ...catalog,
      plan("tier_omega", {
        priceCents: 29900n,
        displayOrder: 5,
        allowedDaemonSizes: ["m", "l", "xl", "2xl", "3xl"],
      }),
    ];
    expect(offeredDaemonSizes(with2xl)).toEqual([
      "m",
      "l",
      "xl",
      "2xl",
      "3xl",
    ]);
  });

  it("still offers no 2xl to a catalog that does not sell one", () => {
    expect(offeredDaemonSizes(catalog)).not.toContain("3xl");
  });

  it("is empty when the catalog names no sizes at all", () => {
    expect(offeredDaemonSizes([plan("tier_bare", { priceCents: 1000n })])).toEqual(
      [],
    );
  });

  it("drops a size name the client does not understand rather than rendering it", () => {
    const odd = [
      plan("tier_odd", { priceCents: 1000n, allowedDaemonSizes: ["m", "titan"] }),
    ];
    expect(offeredDaemonSizes(odd)).toEqual(["m"]);
  });
});
