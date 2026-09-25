/**
 * The catalog, turned into ONE ROW PER MACHINE SIZE.
 *
 * ── Why this is a shared function and not two similar loops ───────────
 *
 * Plan and size are ONE axis. Every compute plan's `allowed_daemon_sizes` is a
 * superset of every cheaper plan's (small ⊂ medium ⊂ large ⊂ xl in
 * control-plane's plans.yaml), so "which plans run Medium?" always answers
 * "Medium and everything above it". There is no question a plan list can ask
 * that a size list cannot: picking a size picks the cheapest plan that runs
 * it, which is exactly `smallestPlanAllowingSize`.
 *
 * Two surfaces present that choice — the onboarding compute step and the
 * settings Plans tab — and they had drifted into two different answers to it.
 * Onboarding walked the sizes; settings listed the catalog by plan name
 * BESIDE a size filter that could only ever dim the cheaper cards, because a
 * strictly-nested ladder has nothing to filter. The owner's report was that
 * the filter was pointless and that "onboarding does a better job", and both
 * halves of that are this function.
 *
 * So the derivation lives once. A caller supplies the catalog and decides what
 * a row DOES; what a row IS — which sizes are offered, which plan each one
 * buys, what it costs and what hardware it reserves — is decided here.
 */

import type { Plan } from "@/gen/controlplane/controlplane/v1/shared_pb";
import {
  derivePlanDisplay,
  formatSizeLabel,
  offeredDaemonSizes,
  smallestPlanAllowingSize,
  type DaemonSizeName,
} from "@/components/Settings/cloud/billingUtils";

import { formatMachineSpec } from "./machineSpecs";
import type { ComputePlanOption } from "./PlanTiles";

/**
 * The wire fields this derivation reads. Narrow on purpose — it is the same
 * `Pick` the size helpers in billingUtils take, plus the `id` a row needs to
 * name the plan it buys.
 */
export type MachineCatalogPlan = Pick<
  Plan,
  "id" | "priceCents" | "displayOrder" | "structuredLimits"
>;

/**
 * One row per machine size the catalog actually sells, cheapest-first.
 *
 * Sizes come from the catalog rather than from this client's enum, so a
 * control plane that stops selling XL stops offering it with no frontend
 * change — and a size name this client cannot label is dropped rather than
 * rendered raw (`offeredDaemonSizes`).
 *
 * A size whose cheapest plan arrived unpriced is dropped too. It is the same
 * rule `COMPUTE_PLAN_UNPRICED` exists for: a machine we cannot price must not
 * appear beside a pay button, because the only alternatives are showing "$0.00"
 * for something that costs $79 or showing a blank where a price belongs.
 */
export function deriveMachineOptions<T extends MachineCatalogPlan>(
  plans: T[],
): ComputePlanOption[] {
  const options: ComputePlanOption[] = [];
  for (const size of offeredDaemonSizes(plans)) {
    const plan = smallestPlanAllowingSize(plans, size);
    if (!plan) continue;
    const display = derivePlanDisplay(plan);
    if (display.monthlyPriceCents === null) continue;
    options.push({
      planId: plan.id,
      // The MACHINE names the row, not the plan. A user does not want "Compute
      // Medium", they want a machine that holds their build; the plan is the
      // instrument that unlocks it and is never the thing being chosen.
      label: formatSizeLabel(size),
      // What the size actually BUYS, in its own slot rather than concatenated
      // into the label. "Medium" describes nothing on its own, and the caller
      // that needs the bare name back (the coupon coverage sentence) would
      // otherwise have to unpick the string it just built.
      detail: formatMachineSpec(size),
      size,
      monthlyPriceCents: display.monthlyPriceCents,
      includedMinutes: display.includedMinutes,
      overageCentsPerMinute: display.overageCentsPerMinute,
    });
  }
  return options;
}

export type { DaemonSizeName };
