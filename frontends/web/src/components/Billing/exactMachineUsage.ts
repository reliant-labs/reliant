import { compareRational, displayRatio, parseRational } from "@/lib/exact-rational";
import { hoursAt, measuredMachineUsage, overageWeightedSeconds, type DaemonUsageLike, type HoursReference } from "./daemonUsage";

/**
 * Chart geometry is approximate; all balance arithmetic stays rational until this boundary.
 *
 * Hours are ONE unit everywhere: hours of the plan's own machine size, the
 * unit a customer bought ("160 hours a month"). The server keeps balances in
 * versioned weighted seconds; `reference` is the plan's size and its exact
 * cost weight, which turns them back into that size's hours. Without a
 * reference the labels are null — a weighted figure printed as "hours" would
 * be a different, larger number than the one on the price list.
 */
export function exactUsageDisplay(usage: DaemonUsageLike | undefined, reference?: HoursReference | null) {
  if (!usage || !measuredMachineUsage(usage)) return null;
  const included = parseRational(usage.allowanceWeightedSeconds)!;
  const used = parseRational(usage.consumedWeightedSeconds)!;
  const grant = parseRational(usage.grantRemainingWeightedSeconds)!;
  const overage = overageWeightedSeconds(usage);
  if (!overage) return null;
  const fraction = displayRatio(used, included);
  const includedHours = hoursAt(included, reference);
  const usedHours = hoursAt(used, reference);
  const grantHours = hoursAt(grant, reference);
  return {
    includedLabel: includedHours === null ? null : `${includedHours} hours included`,
    usedLabel: usedHours === null ? null : `${usedHours} hours`,
    /** "62 of 160 hours used" — the one-line summary. */
    usedOfIncludedLabel: usedHours === null || includedHours === null ? null : `${usedHours} of ${includedHours} hours used`,
    grantLabel: grantHours === null ? null : `${grantHours} hours`,
    hasGrant: grant.numerator > 0n,
    fraction,
    // Capacity values are normalized dimensionless geometry, never billable quantities.
    capacity: {
      usedPct: (fraction ?? 0) * 100,
      overagePct: (displayRatio(overage, included) ?? (overage.numerator > 0n ? 1 : 0)) * 100,
      // State comparisons remain exact even if a chart pixel rounds to the edge.
      state: overage.numerator > 0n ? "overage" as const : compareRational(used, included) >= 0 ? "spent" as const : "under" as const,
    },
  };
}
