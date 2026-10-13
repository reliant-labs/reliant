// Copyright (c) 2025 Reliant Labs

/**
 * What the usage view tells the reader, decided in one place and kept free of
 * React so every rung of the ladder is assertable without a DOM. The server
 * decides the STATE (ok / over_100 / over_150) and whether the org pays; this
 * only words it. Nothing here recomputes billing.
 */

import type { ObservabilityUsage } from "@/services/controlPlane/observability/types";

import { formatBytes } from "./format";

/** At or above this share of the allowance the page starts saying so (the server sends its 80% notice at the same point). */
export const NOTICE_FRACTION = 0.8;

export type NoticeTone = "info" | "warning" | "destructive";

export interface UsageNotice {
  tone: NoticeTone;
  title: string;
  body: string;
}

export function formatDollars(cents: number): string {
  const dollars = cents / 100;
  return `$${dollars.toFixed(dollars >= 100 ? 0 : 2)}`;
}

/** "25.0000" → "$0.25". */
export function formatRate(rateCentsPerGb: string): string {
  const cents = Number(rateCentsPerGb);
  return Number.isFinite(cents) ? formatDollars(cents) : "—";
}

export function usedFraction(u: ObservabilityUsage): number {
  return u.allowanceBytes > 0 ? u.usedBytes / u.allowanceBytes : 0;
}

/** Browser data counts toward the bill only up to a monthly cap; true once some of it is not billed. */
export function browserCapHit(u: ObservabilityUsage): boolean {
  return u.browserBytes > u.browserBilledBytes;
}

/**
 * The one message that matters right now, or null when there is nothing to say.
 * Ordered by what the reader must know first: that data is being dropped, then
 * that it is being billed, then that they are close.
 */
export function usageNotice(u: ObservabilityUsage): UsageNotice | null {
  if (u.exempt) {
    return { tone: "info", title: "Observability is not billed for this organization", body: "Usage is still measured and shown here, but nothing is charged and nothing is dropped." };
  }

  const allowance = formatBytes(u.allowanceBytes);

  // What the gateway is doing outranks what usage alone implies: it is the
  // thing that costs the reader data.
  if (u.state === "over_150") {
    return {
      tone: "destructive",
      title: "Over 150% of your allowance: most telemetry is being dropped",
      body: u.spendCapReached
        ? `You reached your ${formatDollars(u.spendCapCents)} monthly spend cap. Only error logs, error spans and metrics are kept until the month resets.`
        : `Only error logs, error spans and metrics are kept. Add a payment method in Settings → Billing to keep everything and pay ${formatRate(u.rateCentsPerGb)}/GB beyond ${allowance}.`,
    };
  }
  if (u.state === "over_100") {
    return {
      tone: "warning",
      title: "Over your allowance: some telemetry is being dropped",
      body: u.spendCapReached
        ? `You reached your ${formatDollars(u.spendCapCents)} monthly spend cap. Debug logs and routine traces are dropped; errors and metrics are kept until the month resets.`
        : `Debug logs and routine traces are dropped; errors and metrics are kept. Add a payment method in Settings → Billing to keep everything and pay ${formatRate(u.rateCentsPerGb)}/GB beyond ${allowance}.`,
    };
  }

  // Not dropping. If usage is over the allowance, the only reason is that it is paid for.
  if (u.usageState !== "ok" && u.usageState !== "unknown") {
    return {
      tone: "info",
      title: `Past your ${allowance} allowance, billed at ${formatRate(u.rateCentsPerGb)}/GB`,
      body: `${formatDollars(u.overageCents)} so far this month, up to a ${formatDollars(u.spendCapCents)} monthly cap. Nothing is dropped.`,
    };
  }

  if (usedFraction(u) >= NOTICE_FRACTION) {
    return {
      tone: "warning",
      title: `You have used ${Math.floor(usedFraction(u) * 100)}% of your ${allowance} allowance`,
      body: u.funded
        ? `Beyond it, usage is billed at ${formatRate(u.rateCentsPerGb)}/GB, up to ${formatDollars(u.spendCapCents)} a month.`
        : `Beyond it, debug logs and routine traces are dropped. Add a payment method in Settings → Billing to pay ${formatRate(u.rateCentsPerGb)}/GB instead.`,
    };
  }
  return null;
}

/** "Measured through 3:10 PM" and, when it has not caught up, why. */
export function measuredThroughLabel(u: ObservabilityUsage, now: number = Date.now()): string {
  if (u.measuredThroughMs === null) return "Usage is measured hourly; the first figures appear within about an hour of data arriving.";
  const time = new Date(u.measuredThroughMs).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
  const lagMin = Math.max(0, Math.round((now - u.measuredThroughMs) / 60_000));
  return `Measured through ${time} (${lagMin < 90 ? "about an hour behind real time" : `${Math.round(lagMin / 60)} hours behind`}).`;
}
