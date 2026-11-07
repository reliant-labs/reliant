import { describe, expect, it } from "vitest";

import { deriveMachineOptions, oneRowPerPlan } from "../machineOptions";

// Pricing v2 (control-plane internal/plansconfig/pricing.yaml): S1 is off
// sale, so the cheapest plan for BOTH s1 and s2 is plan_compute_s2.
const catalog = [
  { id: "plan_compute_s2", displayOrder: 1, priceCents: 2148n, structuredLimits: { allowedDaemonSizes: ["s1", "s2"], machineIncludedHours: 160n } },
  { id: "plan_compute_m", displayOrder: 2, priceCents: 3236n, structuredLimits: { allowedDaemonSizes: ["s1", "s2", "m"], machineIncludedHours: 160n } },
] as unknown as Parameters<typeof deriveMachineOptions>[0];

describe("oneRowPerPlan", () => {
  it("is the bug's precondition: per-size rows share a plan id", () => {
    const rows = deriveMachineOptions(catalog);
    expect(rows.map((r) => [r.size, r.planId])).toEqual([
      ["s1", "plan_compute_s2"],
      ["s2", "plan_compute_s2"],
      ["m", "plan_compute_m"],
    ]);
  });

  it("keeps one row per plan, named by the largest size it runs", () => {
    const rows = oneRowPerPlan(deriveMachineOptions(catalog));
    expect(rows.map((r) => [r.label, r.planId, r.monthlyPriceCents])).toEqual([
      ["S2", "plan_compute_s2", 2148],
      ["M", "plan_compute_m", 3236],
    ]);
    expect(new Set(rows.map((r) => r.planId)).size).toBe(rows.length);
  });

  it("keeps S1 when the catalog sells an S1 plan of its own", () => {
    const withS1 = [
      { id: "plan_compute_s1", displayOrder: 0, priceCents: 1700n, structuredLimits: { allowedDaemonSizes: ["s1"], machineIncludedHours: 160n } },
      ...catalog,
    ] as unknown as Parameters<typeof deriveMachineOptions>[0];
    expect(oneRowPerPlan(deriveMachineOptions(withS1)).map((r) => r.label)).toEqual(["S1", "S2", "M"]);
  });
});
