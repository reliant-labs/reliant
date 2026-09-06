import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import type { Plan, PlanLimits } from "@/gen/controlplane/v1/public/shared_pb";
import {
  COMPUTE_PLAN_UNPRICED,
  derivePlanDisplay,
  isPurchasableComputePlan,
  sortPlansForDisplay,
} from "../billingUtils";

/**
 * The client must render the SERVER's price, never a constant of its own.
 *
 * Before this, three hardcoded tables lived in billingUtils: a per-plan-id
 * monthly price, a per-plan-id overage rate, and an allowlist that both
 * filtered and ordered the plan grid. The price a user saw was therefore a
 * second, independent declaration of what a plan costs, reconciled against
 * what Stripe actually charges by nothing at all. The redesign puts a real
 * card form inches from that number, so it had to go first.
 *
 * These tests pin the replacement in two ways: behaviourally (a plan's
 * rendered facts track the server payload, including for values that no
 * hardcoded table ever had) and structurally (the source no longer contains a
 * per-plan-id price map). The structural half exists because the behavioural
 * half cannot fail if someone reintroduces a fallback that happens to agree
 * with the fixture.
 */

/** Build a Plan the way the wire delivers one. */
function plan(
  id: string,
  fields: { priceCents?: bigint; displayOrder?: number } & Partial<PlanLimits>,
): Plan {
  const {
    priceCents = 0n,
    displayOrder = 0,
    ...limits
  } = fields;
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

describe("derivePlanDisplay reads the server, not a table", () => {
  it("returns the server's price, overage, minutes and sizes verbatim", () => {
    const d = derivePlanDisplay(
      plan("plan_compute_medium", {
        priceCents: 4000n,
        allowedDaemonSizes: ["small", "medium"],
        daemonComputeIncludedMinutes: 2500,
        daemonOveragePerMinuteCents: 0.4,
      }),
    );

    expect(d.monthlyPriceCents).toBe(4000);
    expect(d.overageCentsPerMinute).toBe(0.4);
    expect(d.includedMinutes).toBe(2500);
    expect(d.allowedSizes).toEqual(["small", "medium"]);
  });

  /**
   * The load-bearing one. These values match no hardcoded table that ever
   * existed, so a reintroduced fallback keyed on plan id cannot satisfy it —
   * whereas the case above would still pass against the old $40 constant
   * purely by coincidence.
   */
  it("tracks a repriced plan rather than a per-id constant", () => {
    const d = derivePlanDisplay(
      plan("plan_compute_medium", {
        priceCents: 5500n,
        daemonOveragePerMinuteCents: 0.55,
        daemonComputeIncludedMinutes: 3000,
      }),
    );

    expect(d.monthlyPriceCents).toBe(5500);
    expect(d.overageCentsPerMinute).toBe(0.55);
    expect(d.includedMinutes).toBe(3000);
  });

  /**
   * A plan the server did not price must render as UNAVAILABLE, not as a
   * guess and not as $0.00. Withholding a price is recoverable; showing a
   * confident wrong one next to a pay button is not.
   */
  it("reports an unpriced plan as unavailable rather than free", () => {
    const d = derivePlanDisplay(plan("plan_code_free", { priceCents: 0n }));
    expect(d.monthlyPriceCents).toBe(COMPUTE_PLAN_UNPRICED);
    expect(d.monthlyPriceCents).toBeNull();
  });

  it("reports an absent plan as unavailable", () => {
    expect(derivePlanDisplay(null).monthlyPriceCents).toBeNull();
    expect(derivePlanDisplay(undefined).monthlyPriceCents).toBeNull();
  });

  it("preserves the unlimited sentinel instead of clamping it", () => {
    const d = derivePlanDisplay(
      plan("plan_compute_xl", {
        priceCents: 16000n,
        daemonComputeIncludedMinutes: -1,
      }),
    );
    expect(d.includedMinutes).toBe(-1);
  });
});

describe("plan grid membership and ordering come from the server", () => {
  it("orders by the server's display_order, not a client allowlist", () => {
    const plans = [
      plan("plan_compute_large", { priceCents: 8000n, displayOrder: 3 }),
      plan("plan_compute_small", { priceCents: 2000n, displayOrder: 1 }),
      plan("plan_compute_medium", { priceCents: 4000n, displayOrder: 2 }),
    ];
    expect(sortPlansForDisplay(plans).map((p) => p.id)).toEqual([
      "plan_compute_small",
      "plan_compute_medium",
      "plan_compute_large",
    ]);
  });

  /**
   * The allowlist's real cost: a plan added to the catalog was invisible in
   * the UI until someone edited the frontend. A newly added tier must appear
   * with no client change at all.
   */
  it("includes a plan id the client has never heard of", () => {
    const brandNew = plan("plan_compute_titan", {
      priceCents: 32000n,
      displayOrder: 5,
      allowedDaemonSizes: ["small", "medium", "large", "xl"],
    });
    expect(isPurchasableComputePlan(brandNew)).toBe(true);
    expect(derivePlanDisplay(brandNew).monthlyPriceCents).toBe(32000);
  });

  it("excludes plans with no monthly price from the purchase grid", () => {
    expect(isPurchasableComputePlan(plan("plan_code_free", {}))).toBe(false);
    expect(
      isPurchasableComputePlan(plan("plan_compute_free", { priceCents: 0n })),
    ).toBe(false);
  });
});

/**
 * Structural guard. The behavioural tests above cannot detect a fallback
 * reintroduced with values that happen to match the fixtures, and the failure
 * mode being defended against is exactly "someone adds a sensible-looking
 * default so the UI stops showing a blank". Read the source and refuse the
 * shape: a map keyed by plan id holding money.
 */
describe("no client-side price table may return", () => {
  // Resolved from the vitest root (web/) rather than import.meta.url, which
  // is not a file: URL under this config.
  //
  // The glob covers `overview/` as well as billingUtils, because the redesign
  // moved rendering into per-product band components — and a band that quietly
  // grew its own `{ plan_compute_small: 2000 }` would be the same defect in a
  // new file, which a guard scoped to one path would never see.
  const sourceDir = resolve(process.cwd(), "src/components/Settings/cloud");
  const sources = [
    "billingUtils.ts",
    "overview/CreditBand.tsx",
    "overview/ComputeBand.tsx",
    "overview/StatusLine.tsx",
  ].map((file) => ({
    file,
    text: readFileSync(resolve(sourceDir, file), "utf8"),
  }));
  const source = sources.map((s) => s.text).join("\n");

  it("declares no per-plan-id price or overage map", () => {
    // e.g. `plan_compute_small: 2000` — a plan id mapped to a number.
    const planIdToNumber = /plan_[a-z_]+\s*:\s*-?[\d_.]+/g;
    expect(source.match(planIdToNumber) ?? []).toEqual([]);
  });

  it("names no compute plan ids at all", () => {
    // The allowlist was `COMPUTE_PLAN_IDS = ["plan_compute_small", ...]`.
    // Membership is now a server fact, so the client should not know any
    // specific plan id.
    expect(source.match(/"plan_compute_[a-z]+"/g) ?? []).toEqual([]);
  });
});
