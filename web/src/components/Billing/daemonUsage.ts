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
 * The usage headline: "41.5 hours left".
 *
 * NULL WHEN UNMEASURED. "0.0 hours left" from a server that did
 * not measure is the unknown-as-known defect usage_measured exists to close,
 * and here it would tell a user they are out when they may not be.
 */
export function remainingHeadline(usage: DaemonUsageLike | undefined): string | null {
  if (!usage || !usage.usageMeasured) return null;
  return `${hours(Number(usage.remainingSmallDaemonSeconds))} hours left`;
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
 * What a running hour of this size costs once plan hours are used up, from
 * the server's price list. Null when the server sent no price
 * for the size, so a card shows no price rather than a guessed one.
 */
export function hourlyPriceShort(pricing: DaemonPricingLike | undefined, size: string): string | null {
  const row = pricing?.sizes.find((s) => s.size === size);
  if (!row || !Number.isFinite(Number(row.hourlyPriceCents))) return null;
  return `${centsDecimalToDollars(row.hourlyPriceCents)}/hr`;
}

/** Running time and retained disks have separate meters. */
export const MACHINE_BILLING_POLICY = "Running machines use included hours, even when idle. Suspended machines use no hours but keep their disk, billed until deleted.";

/**
 * Hours of `size` that `includedSmallHours` buys: the plan's allowance is in
 * small-machine hours, and a size uses them at its server multiplier. Null
 * when either input is missing — an allowance is never guessed.
 */
export function sizeHours(
  pricing: DaemonPricingLike | undefined,
  size: string,
  includedSmallHours: number | undefined,
): string | null {
  const multiplier = Number(pricing?.sizes.find((s) => s.size === size)?.multiplier);
  if (includedSmallHours === undefined || !Number.isFinite(includedSmallHours) || includedSmallHours < 0) return null;
  if (!Number.isFinite(multiplier) || multiplier <= 0) return null;
  // Floor to a tenth so the footnote never promises more than the plan includes.
  const hrs = Math.floor((includedSmallHours / multiplier) * 10) / 10;
  return hrs.toLocaleString("en-US", { maximumFractionDigits: 1 });
}

/**
 * The hours a plan includes, at the plan's OWN size: its largest allowed size.
 * Null when the allowance or that size's multiplier is unknown.
 */
export function planOwnSizeHours(
  pricing: DaemonPricingLike | undefined,
  allowedSizes: string[],
  includedSmallHours: number | undefined,
): string | null {
  const own = (pricing?.sizes ?? [])
    .filter((s) => allowedSizes.includes(s.size))
    .reduce<DaemonSizePriceLike | undefined>((top, s) => (!top || Number(s.multiplier) > Number(top.multiplier) ? s : top), undefined);
  return own ? sizeHours(pricing, own.size, includedSmallHours) : null;
}

/** The one footnote under every machine picker. */
export function machinePricingFootnote(hours: string | null): string {
  return `${hours ? `Includes ${hours} hours a month. ` : ""}Extra hours and storage are billed separately.`;
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
