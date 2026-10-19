/**
 * Per-daemon billing, the client half (task J, design §6.2).
 *
 * EVERY NUMBER HERE COMES FROM THE SERVER. The allowance, what is used, the
 * burn rate, when it runs out, each size's multiplier and price, and the disk
 * fee are read from GetCurrentUserComputeUsage's §6.1 fields and ListPlans'
 * `daemonPricing` — never restated. A client that knew "Large is 4×" would be
 * the second definition the design exists to prevent, and it would be wrong
 * the day the catalog changed.
 *
 * The unit is the SMALL-DAEMON-SECOND (SDS): one second of a 1× machine. A
 * Large (4×) burns four per second. Before the cutover the server reports the
 * old minute model in the same unit (minutes × 60), so these labels read the
 * same either side of it.
 *
 * Pure functions only, so every sentence a user reads before paying is a unit
 * test.
 */

/** One size's row of the server's price list. */
export interface DaemonSizePriceLike {
  size: string;
  multiplier: bigint | number;
  /** Exact decimal string, cents per wall-clock hour. */
  hourlyPriceCents: string;
  storageGib: bigint | number;
}

/** The server's per-daemon price list (ListPlansResponse.daemon_pricing). */
export interface DaemonPricingLike {
  sizes: DaemonSizePriceLike[];
  /** Exact decimal string, cents per GiB-month. */
  suspendedDiskCentsPerGibMonth: string;
  placeholder: boolean;
}

interface TimestampLike {
  seconds: bigint | number;
}

/** The §6.1 slice of a usage response these labels read. */
export interface DaemonUsageLike {
  usageMeasured: boolean;
  includedSmallDaemonSeconds: bigint | number;
  usedSmallDaemonSeconds: bigint | number;
  remainingSmallDaemonSeconds: bigint | number;
  runningMultiplier: bigint | number;
  exhaustsAt?: TimestampLike;
  measuredThrough?: TimestampLike;
  bySize: { size: string; multiplier: bigint | number; runningSeconds: bigint | number; overageSeconds: bigint | number }[];
}

const SDS_PER_HOUR = 3600;

/** "41.5" — hours to one decimal, trailing ".0" kept for a stable width. */
function hours(sds: number): string {
  return (sds / SDS_PER_HOUR).toFixed(1);
}

/** "Large" from "large", "2XL" from "2xl". */
export function sizeName(size: string): string {
  if (/^\d*xl$/i.test(size)) return size.toUpperCase();
  return size.charAt(0).toUpperCase() + size.slice(1);
}

/**
 * The usage headline: "41.5 small-daemon-hours left".
 *
 * NULL WHEN UNMEASURED. "0.0 small-daemon-hours left" from a server that did
 * not measure is the unknown-as-known defect usage_measured exists to close,
 * and here it would tell a user they are out when they may not be.
 */
export function remainingHeadline(usage: DaemonUsageLike | undefined): string | null {
  if (!usage || !usage.usageMeasured) return null;
  return `${hours(Number(usage.remainingSmallDaemonSeconds))} small-daemon-hours left`;
}

/**
 * The burn line: "≈ 10.4 h on your Large (4×)".
 *
 * Remaining SDS divided by the current burn rate is wall-clock hours at the
 * current rate. It names a size ONLY when that is unambiguous: the period ran
 * exactly one size, and the current rate is that size's multiplier (one of
 * them is up). Anything else — two machines up, or a period that mixed sizes —
 * and "on your Large" would be a guess, so it says "at the current rate".
 * Null when nothing runs (no rate to burn at) or when unmeasured.
 */
export function burnLabel(usage: DaemonUsageLike | undefined): string | null {
  if (!usage || !usage.usageMeasured) return null;
  const rate = Number(usage.runningMultiplier);
  if (rate <= 0) return null;
  const wallHours = hours(Number(usage.remainingSmallDaemonSeconds) / rate);
  const only = usage.bySize.length === 1 ? usage.bySize[0] : undefined;
  if (only && Number(only.multiplier) === rate) {
    return `≈ ${wallHours} h left on your ${sizeName(only.size)}`;
  }
  return `≈ ${wallHours} h left with your running machines`;
}

/**
 * "runs out ~Thu at the current rate", from exhausts_at.
 *
 * Null when the server sent no exhausts_at — nothing is running, or nothing
 * remains — never a fabricated date. Within a day it gives the time, beyond a
 * week the date.
 */
export function runsOutLabel(usage: DaemonUsageLike | undefined, now: Date = new Date()): string | null {
  if (!usage || !usage.usageMeasured || !usage.exhaustsAt) return null;
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
  if (!usage || !usage.usageMeasured || !usage.measuredThrough) return null;
  const at = new Date(Number(usage.measuredThrough.seconds) * 1000);
  return `as of ${at.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })}`;
}

/** Exact-decimal cents string → dollars, "$0.13". */
export function centsDecimalToDollars(cents: string): string {
  const value = Number(cents);
  if (!Number.isFinite(value)) return "—";
  return `$${(value / 100).toFixed(2)}`;
}

/**
 * The monthly suspended-disk fee for `storageGib` GiB: storage × the per-GiB-
 * month fee. "$6.25/mo".
 */
export function suspendedFeeLabel(pricing: DaemonPricingLike | undefined, storageGib: number): string | null {
  if (!pricing || storageGib <= 0) return null;
  const perGib = Number(pricing.suspendedDiskCentsPerGibMonth);
  if (!Number.isFinite(perGib)) return null;
  return `${centsDecimalToDollars(String(perGib * storageGib))}/mo`;
}

/**
 * One size's hourly price, short enough for a size card: "$0.26/hr".
 *
 * The same number `sizeFacts.hourlyPriceLabel` states in a sentence — what a
 * running hour of this size costs once included hours are used up — from the
 * same row of the server's price list. Null when the server sent no price
 * for the size, so a card shows no price rather than a guessed one.
 */
export function hourlyPriceShort(pricing: DaemonPricingLike | undefined, size: string): string | null {
  const row = pricing?.sizes.find((s) => s.size === size);
  if (!row || !Number.isFinite(Number(row.hourlyPriceCents))) return null;
  return `${centsDecimalToDollars(row.hourlyPriceCents)}/hr`;
}

/** Running time and retained disks have separate meters. */
export const MACHINE_BILLING_POLICY = "Running machines use included hours, even when idle. Suspended machines use no hours but keep their disk, billed until deleted.";
export const MACHINE_PRICING_NOTE = "Included runtime is shared across your machines, not a separate allowance for each size. Machines pause when included hours run out unless you enable overage. The standard disk is included while running; extra disk is billed separately.";

/** Monthly entitlement, NOT remaining usage. No catalog/legacy-minute fallback. */
export function includedRuntimeLabel(
  pricing: DaemonPricingLike | undefined,
  size: string,
  includedSmallDaemonHours: number | undefined,
): string {
  const multiplier = Number(pricing?.sizes.find((s) => s.size === size)?.multiplier);
  if (includedSmallDaemonHours === undefined || !Number.isFinite(includedSmallDaemonHours) || includedSmallDaemonHours < 0 || !Number.isFinite(multiplier) || multiplier <= 0) {
    return "Included runtime unavailable";
  }
  const runtime = includedSmallDaemonHours / multiplier;
  // Round down to avoid promising more time than the server includes.
  const formatted = (Math.floor(runtime * 10) / 10).toLocaleString("en-US", { maximumFractionDigits: 1 });
  return `${formatted} ${sizeName(size)} hours/month included`;
}

/** DaemonDisk stays in DATA GiB, including after whole-disk billing (#1000).
 * The server price list minus included data gives the allowance: zero before
 * cutover, the configured docker/cache allowance after it. Never hardcode it.
 */
export function diskAllowanceGib(pricing: DaemonPricingLike | undefined, size: string, includedGib: number): number | null {
  const row = pricing?.sizes.find((s) => s.size === size);
  return row && includedGib > 0 ? Math.max(0, Number(row.storageGib) - includedGib) : null;
}

export function provisionedDiskGib(pricing: DaemonPricingLike | undefined, size: string, disk: { capacityGib: number; includedGib: number } | undefined): number | null {
  if (!disk || disk.capacityGib <= 0) return null;
  const allowance = diskAllowanceGib(pricing, size, disk.includedGib);
  return allowance === null ? null : disk.capacityGib + allowance;
}

/** One size's picker facts: price, burn rate, disk, and its suspended fee. */
export interface SizeFacts {
  hourlyPriceLabel: string;
  includedRuntimeLabel: string;
  diskLabel: string;
  suspendedFeeLabel: string | null;
}

export function sizeFacts(pricing: DaemonPricingLike | undefined, size: string, includedSmallDaemonHours?: number): SizeFacts | null {
  const row = pricing?.sizes.find((s) => s.size === size);
  if (!pricing || !row) return null;
  const storage = Number(row.storageGib);
  return {
    hourlyPriceLabel: `${centsDecimalToDollars(row.hourlyPriceCents)}/h after included hours, with overage enabled`,
    includedRuntimeLabel: includedRuntimeLabel(pricing, size, includedSmallDaemonHours),
    diskLabel: `${storage} GiB disk`,
    suspendedFeeLabel: suspendedFeeLabel(pricing, storage),
  };
}

/** Concrete alternatives for eligible sizes, all drawing from ONE monthly pool. */
export function includedHoursSentence(
  includedSmallDaemonHours: number | undefined,
  pricing: DaemonPricingLike | undefined,
  allowedSizes?: string[],
): string {
  const sizes = pricing?.sizes.filter((s) => allowedSizes?.includes(s.size)) ?? [];
  if (includedSmallDaemonHours === undefined || sizes.length === 0) return "Included runtime unavailable. Runtime is shared across your machines.";
  const alternatives = sizes.map((s) => includedRuntimeLabel(pricing, s.size, includedSmallDaemonHours).replace("/month included", ""));
  return `Each month: ${alternatives.join(" or ")}. Shared across your machines, not a separate allowance for each size.`;
}
