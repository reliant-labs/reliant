import { compareRational, displayRatio, divideRational, formatRational, parseRational, rational } from "@/lib/exact-rational";
import { measuredMachineUsage, overageWeightedSeconds, type DaemonUsageLike } from "./daemonUsage";

/** Chart geometry is approximate; all balance arithmetic stays rational until this boundary. */
export function exactUsageDisplay(usage: DaemonUsageLike | undefined, referenceWeight?: string) {
  if (!usage || !measuredMachineUsage(usage)) return null;
  const included = parseRational(usage.allowanceWeightedSeconds)!;
  const used = parseRational(usage.consumedWeightedSeconds)!;
  const overage = overageWeightedSeconds(usage);
  if (!overage) return null;
  const fraction = displayRatio(used, included);
  const reference = parseRational(referenceWeight);
  const hours = (amount: typeof included) => {
    const seconds = reference ? divideRational(amount, reference) : amount;
    return seconds ? formatRational(divideRational(seconds, rational(3600n)!)!, 1, "floor") : "unavailable";
  };
  const unit = reference ? "equivalent h at plan size" : "weighted h";
  return {
    includedLabel: `${hours(included)} ${unit} included`,
    usedLabel: `≈ ${hours(used)} ${unit}`,
    grantLabel: `${hours(parseRational(usage.grantRemainingWeightedSeconds)!)} ${unit}`,
    hasGrant: parseRational(usage.grantRemainingWeightedSeconds)!.numerator > 0n,
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
