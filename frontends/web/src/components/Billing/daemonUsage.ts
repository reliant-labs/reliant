/**
 * Per-daemon billing, the client half (task J, design §6.2).
 *
 * EVERY NUMBER HERE COMES FROM THE SERVER. The allowance, what is used, the
 * burn rate, when it runs out, each size's multiplier and price, and the
 * storage price are read from GetCurrentUserComputeUsage's §6.1 fields and ListPlans'
 * `daemonPricing` — never restated. A client that knew "Large is 4×" would be
 * the second definition the design exists to prevent, and it would be wrong
 * the day the catalog changed.
 *
 * Allowance accounting uses immutable-version exact weighted seconds. Runtime
 * is native seconds; exchange weights never multiply customer charges.
 *
 * Pure functions only, so every sentence a user reads before paying is a unit
 * test.
 */

import { addRational, compareRational, divideRational, exactCentsLabel, formatRational, multiplyRational, parseRational, rational, type Rational } from "@/lib/exact-rational";

/** One size's row of the server's price list. */
export interface DaemonSizePriceLike {
  size: string;
  costWeight?: string;
  exactHourlyPriceCents?: string;
  /** Deprecated fields are never used for weighted accounting. */
  multiplier?: bigint | number;
  /** Exact decimal string, cents per wall-clock hour. */
  hourlyPriceCents: string;
  /** The size's one disk, in GiB: what a plan of this size includes. */
  storageGib: bigint | number;
}

/** The server's per-daemon price list (ListPlansResponse.daemon_pricing). */
export interface DaemonPricingLike {
  sizes: DaemonSizePriceLike[];
  pricingVersion?: string;
  activationReady?: boolean;
  activationHoldReason?: string;
  /** Exact decimal string, cents per GiB-month of disk above the plan's
   * included storage, running or suspended. */
  storageCentsPerGibMonth: string;
  placeholder: boolean;
}

interface TimestampLike {
  seconds: bigint | number;
}

/** The §6.1 slice of a usage response these labels read. */
export interface DaemonUsageLike {
  usageMeasured: boolean;
  pricingVersion?: string;
  allowanceWeightedSeconds?: string;
  consumedWeightedSeconds?: string;
  remainingWeightedSeconds?: string;
  grantRemainingWeightedSeconds?: string;
  runningCostWeightSum?: string;
  billingHoldReason?: string;
  measuredFrom?: TimestampLike;
  periodStart?: TimestampLike;
  exhaustsAt?: TimestampLike;
  measuredThrough?: TimestampLike;
  bySize: { size: string; pricingVersion?: string; costWeight?: string; exactRunningSeconds?: string; exactOverageSeconds?: string; includedWeightedSeconds?: string; overageCostCents?: string }[];
}


/**
 * Native excess seconds converted to exact versioned weighted units for chart
 * comparison only. This is never a retail-charge calculation.
 */
export function overageWeightedSeconds(usage: Pick<DaemonUsageLike, "bySize">): Rational | null {
  let sum = rational(0n)!;
  for (const row of usage.bySize) {
    const seconds = parseRational(row.exactOverageSeconds);
    const weight = parseRational(row.costWeight);
    if (!seconds || !weight || weight.numerator === 0n) return null;
    sum = addRational(sum, multiplyRational(seconds, weight));
  }
  return sum;
}

export function measuredWindowCost(usage: DaemonUsageLike | undefined): string | null {
  if (!usage || !measuredMachineUsage(usage)) return null;
  let cents = rational(0n)!;
  for (const row of usage.bySize) {
    const cost = parseRational(row.overageCostCents);
    if (!cost) return null;
    cents = addRational(cents, cost);
  }
  return `$${formatRational(divideRational(cents, rational(100n)!)!, 2)}`;
}

export function measuredMachineUsage(usage: DaemonUsageLike | undefined): boolean {
  return !!usage?.usageMeasured && !!usage.pricingVersion && !usage.billingHoldReason &&
    [usage.allowanceWeightedSeconds, usage.consumedWeightedSeconds, usage.remainingWeightedSeconds,
      usage.grantRemainingWeightedSeconds, usage.runningCostWeightSum].every(value => parseRational(value) !== null) &&
    usage.bySize.every(row => row.pricingVersion === usage.pricingVersion &&
      (parseRational(row.costWeight)?.numerator ?? 0n) > 0n &&
      parseRational(row.exactRunningSeconds) !== null && parseRational(row.exactOverageSeconds) !== null);
}

/**
 * The size a customer's hours are counted in, and its exact cost weight.
 *
 * Every hour figure a customer reads is in ONE unit: hours of the plan's own
 * machine size (the largest it runs), because that is what the price list
 * sells ("160 hours a month"). Balances arrive as versioned weighted seconds;
 * dividing by this weight turns them back into those hours.
 */
export interface HoursReference {
  size: string;
  weight: string;
}

/**
 * The size a coupon's hours are counted in when there is no plan: the coupon
 * unit (control-plane pricing.yaml `coupons.unit_size`; a coupon-funded org
 * admits only it). The same fact lib/formatMachineMinutes states as
 * "hours on an M machine".
 */
export const COUPON_UNIT_SIZE = "m";

/** The sizes whose hours a customer is counted in: their plan's, else the coupon unit's. */
export function hoursSizes(planAllowedSizes: string[] | undefined): string[] {
  return planAllowedSizes && planAllowedSizes.length > 0 ? planAllowedSizes : [COUPON_UNIT_SIZE];
}

/**
 * The plan's own size among `allowedSizes` — the one with the largest exact
 * cost weight — or null when the price list is missing, its version differs
 * from the usage's, or none of the sizes is priced.
 */
export function hoursReference(
  pricing: DaemonPricingLike | undefined,
  allowedSizes: string[],
  usage?: Pick<DaemonUsageLike, "pricingVersion">,
): HoursReference | null {
  if (!pricing?.pricingVersion) return null;
  if (usage && usage.pricingVersion !== pricing.pricingVersion) return null;
  const own = pricing.sizes.filter(s => allowedSizes.includes(s.size)).reduce<DaemonSizePriceLike | undefined>((top, row) => {
    const weight = parseRational(row.costWeight);
    const previous = parseRational(top?.costWeight);
    return weight && weight.numerator > 0n && (!previous || compareRational(weight, previous) > 0) ? row : top;
  }, undefined);
  return own?.costWeight ? { size: own.size, weight: own.costWeight } : null;
}

/** Hours to one decimal, rounded down, without a trailing ".0": "160", "33.3". */
function hoursText(hours: Rational): string {
  return formatRational(hours, 1, "floor").replace(/\.0$/, "");
}

/** Weighted seconds as hours of the reference size, or null without one. */
export function hoursAt(weightedSeconds: Rational, reference: HoursReference | null | undefined): string | null {
  const weight = parseRational(reference?.weight);
  if (!weight || weight.numerator === 0n) return null;
  return hoursText(divideRational(divideRational(weightedSeconds, weight)!, rational(3600n)!)!);
}

export function equivalentHours(usage: DaemonUsageLike | undefined, weight: string | undefined, field: "allowanceWeightedSeconds" | "consumedWeightedSeconds" | "remainingWeightedSeconds" | "grantRemainingWeightedSeconds"): string | null {
  if (!measuredMachineUsage(usage)) return null;
  const amount = parseRational(usage?.[field]);
  const rate = parseRational(weight);
  const seconds = amount && rate ? divideRational(amount, rate) : null;
  return seconds ? formatRational(divideRational(seconds, rational(3600n)!)!, 1, "floor") : null;
}

export function usageWindowLabel(usage: DaemonUsageLike): string {
  const from = usage.measuredFrom;
  const partial = from && (!usage.periodStart || BigInt(from.seconds) > BigInt(usage.periodStart.seconds));
  if (!from) return "Machine usage this billing period. Some earlier use may not be shown yet.";
  if (!partial) return "Machine usage this billing period.";
  return `Machine usage since ${new Date(Number(from.seconds) * 1000).toLocaleDateString("en-US", { month: "short", day: "numeric" })}. Earlier use this period isn't shown.`;
}

/** "XL" from "xl", "3XL" from "3xl". */
export function sizeName(size: string): string {
  if (/^\d*xl$/i.test(size)) return size.toUpperCase();
  return size.charAt(0).toUpperCase() + size.slice(1);
}

/**
 * The usage headline: "41.5 hours left".
 *
 * NULL WHEN UNMEASURED. "0.0 hours left" from a server that did
 * not measure is the unknown-as-known defect usage_measured exists to close,
 * and here it would tell a user they are out when they may not be.
 */
export function remainingHeadline(usage: DaemonUsageLike | undefined, reference?: HoursReference | null): string | null {
  if (!measuredMachineUsage(usage)) return null;
  // While machines run, time left at that rate; otherwise the plan's own hours.
  const burn = burnLabel(usage);
  if (burn) return burn;
  const remaining = parseRational(usage?.remainingWeightedSeconds)!;
  if (remaining.numerator === 0n) return "Included hours used up";
  const hours = hoursAt(remaining, reference);
  return hours ? `${hours} hours left` : "Included hours available";
}

/**
 * The burn line: "≈ 10.4 hours left at the current rate".
 *
 * Remaining weighted seconds divided by exact running cost-weight sum yields
 * equivalent wall-clock time. Approximation is display-only. Never infer a size
 * from historical rows or use retail rates to exchange allowance.
 */
export function burnLabel(usage: DaemonUsageLike | undefined): string | null {
  if (!usage || !measuredMachineUsage(usage)) return null;
  const rate = parseRational(usage.runningCostWeightSum)!;
  if (rate.numerator === 0n) return null;
  const seconds = divideRational(parseRational(usage.remainingWeightedSeconds)!, rate)!;
  return `≈ ${hoursText(divideRational(seconds, rational(3600n)!)!)} hours left at the current rate`;
}

/**
 * "runs out ~Thu at the current rate", from exhausts_at.
 *
 * Null when the server sent no exhausts_at — nothing is running, or nothing
 * remains — never a fabricated date. Within a day it gives the time, beyond a
 * week the date.
 */
export function runsOutLabel(usage: DaemonUsageLike | undefined, now: Date = new Date()): string | null {
  if (!usage || !measuredMachineUsage(usage) || !usage.exhaustsAt) return null;
  const at = new Date(Number(usage.exhaustsAt.seconds) * 1000);
  const msAhead = at.getTime() - now.getTime();
  let when: string;
  if (msAhead < 24 * 3600 * 1000) {
    when = at.toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit" });
  } else if (msAhead < 7 * 24 * 3600 * 1000) {
    when = at.toLocaleDateString("en-US", { weekday: "short" });
  } else {
    when = at.toLocaleDateString("en-US", { month: "short", day: "numeric" });
  }
  return `runs out ~${when} at the current rate`;
}

/** "as of 14:32", from measured_through. Null when absent or unmeasured. */
export function asOfLabel(usage: DaemonUsageLike | undefined): string | null {
  if (!usage || !measuredMachineUsage(usage) || !usage.measuredThrough) return null;
  const at = new Date(Number(usage.measuredThrough.seconds) * 1000);
  return `as of ${at.toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit" })}`;
}

/** Exact-decimal cents string → dollars, "$0.13". */
export function centsDecimalToDollars(cents: string): string {
  return exactCentsLabel(cents) ?? "—";
}

/** Monthly storage cost of `gib` GiB at the server's price: "$6.25/mo". */
export function storageCostLabel(pricing: DaemonPricingLike | undefined, gib: number): string | null {
  if (!pricing || gib <= 0) return null;
  const perGib = parseRational(pricing.storageCentsPerGibMonth);
  if (!perGib || !Number.isSafeInteger(gib)) return null;
  const cost = multiplyRational(perGib, rational(BigInt(gib))!);
  return `$${formatRational(divideRational(cost, rational(100n)!)!, 2)}/mo`;
}

/**
 * One size's hourly price, short enough for a size card: "$0.26/hr".
 *
 * What a running hour of this size costs once plan hours are used up, from
 * the server's price list. Null when the server sent no price
 * for the size, so a card shows no price rather than a guessed one.
 */
export function hourlyPriceShort(pricing: DaemonPricingLike | undefined, size: string): string | null {
  const row = pricing?.sizes.find((s) => s.size === size);
  if (!row || !pricing?.pricingVersion || !parseRational(row.exactHourlyPriceCents)) return null;
  return `${centsDecimalToDollars(row.exactHourlyPriceCents!)}/hr`;
}

/** Running time and disk have separate meters. */
export const MACHINE_BILLING_POLICY = "Running machines use included hours, even when idle. Suspended machines use none. Your plan includes storage across all machines; disk beyond it is billed running or suspended.";

/**
 * Equivalent hours at a selected size from the exact versioned weighted balance.
 * Null when either input is missing; never fall back to legacy integer fields.
 */
export function sizeHours(
  pricing: DaemonPricingLike | undefined,
  size: string,
  includedWeightedSeconds: string | undefined,
): string | null {
  const weight = parseRational(pricing?.sizes.find((s) => s.size === size)?.costWeight);
  const included = parseRational(includedWeightedSeconds);
  const seconds = weight && included ? divideRational(included, weight) : null;
  if (!pricing?.pricingVersion || !seconds) return null;
  return hoursText(divideRational(seconds, rational(3600n)!)!);
}

/**
 * The hours a plan includes, at the plan's OWN size: its largest allowed size.
 * Null when the exact balance or versioned cost weight is unknown.
 */
export function planOwnSizeHours(
  pricing: DaemonPricingLike | undefined,
  allowedSizes: string[],
  includedWeightedSeconds: string | undefined,
): string | null {
  const own = hoursReference(pricing, allowedSizes);
  return own ? sizeHours(pricing, own.size, includedWeightedSeconds) : null;
}

/** The one footnote under every machine picker. */
export function machinePricingFootnote(hours: string | null): string {
  return `${hours ? `Includes ${hours} hours a month. ` : ""}Extra hours and storage are billed separately.`;
}

/** A machine's disk as provisioned, in GiB: one disk, used for anything. */
export function provisionedDiskGib(disk: { capacityGib: number } | undefined): number | null {
  return disk && disk.capacityGib > 0 ? disk.capacityGib : null;
}

/** The storage slice of a usage response. */
export interface StorageUsageLike {
  provisionedGib: bigint | number;
  includedGib: bigint | number;
  extraGib: bigint | number;
  costCents: string;
  projectedCostCents: string;
}

/** The storage card's one number: "75 GB", with "60 GB included" beside it. */
export function storageCard(storage: StorageUsageLike | undefined): { value: string; detail: string; extraCost: string | null } | null {
  if (!storage) return null;
  const extra = Number(storage.extraGib);
  return {
    value: `${Number(storage.provisionedGib)} GB`,
    detail: `${Number(storage.includedGib)} GB included`,
    extraCost: extra > 0 ? `${centsDecimalToDollars(storage.projectedCostCents)} this period for ${extra} GB extra` : null,
  };
}

/** The projected bill's one number: "$27.40". Null when unmeasured or absent. */
export function projectedBillLabel(usage: { usageMeasured: boolean; projectedPeriodTotalCents?: string; billingHoldReason?: string; measuredFrom?: TimestampLike; periodStart?: TimestampLike } | undefined): string | null {
  if (!usage || !usage.usageMeasured || usage.billingHoldReason || !usage.projectedPeriodTotalCents) return null;
  // A bridge proves opening consumption, not pre-cutover charges.
  if (usage.measuredFrom && (!usage.periodStart || BigInt(usage.measuredFrom.seconds) > BigInt(usage.periodStart.seconds))) return null;
  return centsDecimalToDollars(usage.projectedPeriodTotalCents);
}

/**
 * Paused because included hours ran out: nothing left in the plan or a coupon,
 * and overage is off. The one state where "Turn on overage" is the fix.
 */
export function hoursRanOut(
  usage: DaemonUsageLike | undefined,
  overageEnabled: boolean,
): boolean {
  if (!usage || !measuredMachineUsage(usage) || overageEnabled) return false;
  return parseRational(usage.allowanceWeightedSeconds)!.numerator > 0n && parseRational(usage.remainingWeightedSeconds)!.numerator === 0n &&
    parseRational(usage.grantRemainingWeightedSeconds)!.numerator === 0n;
}
