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
    return `≈ ${wallHours} h on your ${sizeName(only.size)} (${rate}×)`;
  }
  return `≈ ${wallHours} h at the current rate (${rate}×)`;
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

/** One size's picker facts: price, burn rate, disk, and its suspended fee. */
export interface SizeFacts {
  hourlyPriceLabel: string;
  burnRateLabel: string;
  diskLabel: string;
  suspendedFeeLabel: string | null;
}

export function sizeFacts(pricing: DaemonPricingLike | undefined, size: string): SizeFacts | null {
  const row = pricing?.sizes.find((s) => s.size === size);
  if (!pricing || !row) return null;
  const multiplier = Number(row.multiplier);
  const storage = Number(row.storageGib);
  return {
    hourlyPriceLabel: `${centsDecimalToDollars(row.hourlyPriceCents)}/h past your included hours`,
    burnRateLabel:
      multiplier === 1 ? "uses included hours 1× (1 hour per hour)" : `uses included hours ${multiplier}× as fast`,
    diskLabel: `${storage} GiB disk`,
    suspendedFeeLabel: suspendedFeeLabel(pricing, storage),
  };
}

/**
 * The plan tile's allowance sentence (design §6.2):
 * "N small-daemon-hours each month. Medium uses them 2× as fast, Large 4×, …
 *  Suspended machines: $D per GiB-month."
 *
 * Replaces "N machine hours … on every size", which stopped being true the
 * moment sizes burned at different rates.
 */
export function includedHoursSentence(
  includedSmallDaemonHours: number,
  pricing: DaemonPricingLike | undefined,
): string {
  const parts = [`${includedSmallDaemonHours} small-daemon-hours each month.`];
  const faster = (pricing?.sizes ?? []).filter((s) => Number(s.multiplier) > 1);
  if (faster.length > 0) {
    const [first, ...rest] = faster;
    parts.push(
      `${[`${sizeName(first.size)} uses them ${Number(first.multiplier)}× as fast`, ...rest.map((s) => `${sizeName(s.size)} ${Number(s.multiplier)}×`)].join(", ")}.`,
    );
  }
  if (pricing) {
    parts.push(`Suspended machines: ${centsDecimalToDollars(pricing.suspendedDiskCentsPerGibMonth)} per GiB-month.`);
  }
  return parts.join(" ");
}
