/**
 * Prices as the price list writes them.
 *
 * Every figure here comes from the server; this module only decides how a
 * person reads it. The landing page writes "$25/mo", "$0.07/vCPU-hr" and
 * "$0.15/GB after 10 GB free", so the app does too: whole dollars without
 * ".00", a rate with as many decimals as it needs (never a sixteen-digit
 * fraction), and the unit a customer buys in.
 *
 * Pure functions only, so every price sentence is a unit test.
 */

import { divideRational, formatRational, multiplyRational, parseRational, rational, type Rational } from "@/lib/exact-rational";
import { formatMachineMinutes } from "@/lib/formatMachineMinutes";

/** Mean hours in a month, matching control-plane plansconfig.HoursPerMonth. */
export const HOURS_PER_MONTH = 730n;

/**
 * A list price in whole cents: "$25", "$1,249", "$12.50".
 *
 * Whole dollars print bare, the way the price list prints them. Anything with
 * cents keeps both digits, so "$12.5" never appears.
 */
export function formatPrice(cents: number | bigint): string {
  const whole = typeof cents === "bigint" ? cents : BigInt(Math.round(cents));
  const negative = whole < 0n;
  const abs = negative ? -whole : whole;
  const dollars = (abs / 100n).toLocaleString("en-US");
  const rest = abs % 100n;
  return `${negative ? "-" : ""}$${dollars}${rest === 0n ? "" : `.${rest.toString().padStart(2, "0")}`}`;
}

/**
 * An exact cents amount as dollars with only the decimals it needs: "$0.07",
 * "$0.012", "$0.25", "$5". Rounded to the nearest 1/100 of a cent, which is
 * finer than any published rate, so a rate the server derived through a
 * division ("0.0342465753424658" ¢/GiB-hour × 730) reads as the round price
 * it came from.
 */
export function formatRateDollars(cents: Rational): string {
  const fixed = formatRational(divideRational(cents, rational(100n)!)!, 4);
  const [whole, fraction = ""] = fixed.split(".");
  const trimmed = fraction.replace(/0+$/, "");
  if (!trimmed) return `$${whole}`;
  return `$${whole}.${trimmed.padEnd(2, "0")}`;
}

/** One rate row of GetCurrentUserUsageBilling. */
export interface HostedRateLike {
  resourceKind: string;
  displayName: string;
  unit: string;
  /** Exact decimal cents per `unit`. */
  priceCents: string;
  /** Exact decimal quantity of `unit` included free each month, when any. */
  includedMonthly?: string;
}

/** The server's unit as the price list writes it, after "$X/". */
const UNIT_TEXT: Record<string, string> = {
  "vCPU-hour": "vCPU-hr",
  "GiB-hour": "GiB-hr",
  "GiB-month": "GiB-month",
  // A transfer quantity: the price list counts egress in GB.
  GiB: "GB",
};

/**
 * "Hosted CPU: $0.07/vCPU-hr", "Internet egress: $0.15/GB after 10 GB free",
 * "Extra static sites: $5/month each".
 *
 * Null when the price does not parse: the caller must then treat the rates as
 * unavailable and refuse consent, never show a blank where a price belongs.
 */
export function hostedRateLine(rate: HostedRateLike): string | null {
  let cents = parseRational(rate.priceCents);
  if (!cents) return null;
  let unit = rate.unit;
  // Storage is held for a month, and sold per GiB-month. The meter's
  // GiB-hour rate is that price divided by the hours in a month.
  if (rate.resourceKind === "storage_gib" && unit === "GiB-hour") {
    cents = multiplyRational(cents, rational(HOURS_PER_MONTH)!);
    unit = "GiB-month";
  }
  const price = formatRateDollars(cents);
  if (unit === "site-month") return `${rate.displayName}: ${price}/month each (your first site is free)`;
  const unitText = UNIT_TEXT[unit] ?? unit;
  const included = parseRational(rate.includedMonthly);
  const free = included && included.numerator > 0n ? ` after ${formatRational(included, 0)} ${unitText} free each month` : "";
  return `${rate.displayName}: ${price}/${unitText}${free}`;
}

/**
 * Every rate line, or an empty list when any rate is unreadable — consent to
 * a partial price list is not consent.
 */
export function hostedRateLines(rates: HostedRateLike[]): string[] {
  const lines = rates.map(hostedRateLine);
  return lines.every((line): line is string => line !== null) ? lines : [];
}

/** "A $5 minimum applies in any month with hosting usage.", or null without one. */
export function hostingMinimumLine(minimumCents: bigint | number | undefined): string | null {
  if (minimumCents === undefined || BigInt(minimumCents) <= 0n) return null;
  return `A ${formatPrice(minimumCents)} minimum applies in any month with hosting usage.`;
}

/** What a compute coupon added, said in hours on the machine it runs: M. */
export function computeCouponRedeemedLine(minutes: number): string {
  if (!Number.isFinite(minutes) || minutes <= 0) return "Coupon redeemed. Your machine time balance has been updated.";
  return `Added ${formatMachineMinutes(minutes)}. They're one-time, and used after any hours your plan includes.`;
}
