/**
 * Pure formatters + small error helpers for the cloud billing settings
 * section. No React, no fetch — ported from control-plane admin-web's
 * `lib/billing.ts` so the in-app `/settings/billing` dashboard renders the
 * same plan / wallet / usage strings the hosted admin UI does.
 *
 * Wallet balances cross the wire in three field widths (cents / micros /
 * nanos); `nanosFromFields` collapses them onto a single nanos axis so
 * formatting code never has to care which the backend populated.
 */

import { ConnectError, Code } from "@connectrpc/connect";
import type { Plan } from "@/gen/controlplane/v1/public/shared_pb";

export type WalletBalanceState = "healthy" | "low" | "empty";

export const LOW_CREDIT_BALANCE_CENTS = 250;
export const LOW_CREDIT_BALANCE_NANOS =
  BigInt(LOW_CREDIT_BALANCE_CENTS) * BigInt(10_000_000);

// Preset top-up amounts (cents). Stripe's minimum charge is $0.50; these
// mirror the amounts admin-web's WalletBalanceCard offered.
export const TOPUP_PRESETS_CENTS = [1000, 2500, 5000, 10000] as const;

// ── Wallet balance ────────────────────────────────────────────────────

export function nanosFromFields(
  nanos?: bigint | number,
  micros?: bigint | number,
  cents?: bigint | number,
): bigint {
  if (nanos !== undefined && nanos !== 0 && nanos !== BigInt(0)) {
    return typeof nanos === "bigint" ? nanos : BigInt(Math.trunc(nanos));
  }
  if (micros !== undefined && micros !== 0 && micros !== BigInt(0)) {
    const microsValue =
      typeof micros === "bigint" ? micros : BigInt(Math.trunc(micros));
    return microsValue * BigInt(1_000);
  }
  const centsValue =
    typeof cents === "bigint" ? cents : BigInt(Math.trunc(Number(cents ?? 0)));
  return centsValue * BigInt(10_000_000);
}

export function formatCurrencyFromNanos(nanos?: bigint | number): string {
  if (nanos === undefined) return "$0.00";
  const nanosValue =
    typeof nanos === "bigint" ? nanos : BigInt(Math.trunc(nanos));
  const rounded = Number(nanosValue) / 1_000_000_000;
  return `$${rounded.toFixed(2)}`;
}

export function formatCurrencyFromWalletFields(
  nanos?: bigint | number,
  micros?: bigint | number,
  cents?: bigint | number,
): string {
  return formatCurrencyFromNanos(nanosFromFields(nanos, micros, cents));
}

export function getWalletBalanceState(balanceNanos: bigint): WalletBalanceState {
  if (balanceNanos <= BigInt(0)) return "empty";
  if (balanceNanos <= LOW_CREDIT_BALANCE_NANOS) return "low";
  return "healthy";
}

export function getWalletWarning(
  state: WalletBalanceState,
): { title: string; message: string } | null {
  if (state === "healthy") return null;
  return state === "empty"
    ? {
        title: "Credits required",
        message:
          "Your wallet is empty. New AI requests can fail until you add credits.",
      }
    : {
        title: "Low credit balance",
        message:
          "Your remaining credits are running low. Add funds now to avoid interrupted AI requests.",
      };
}

// ── Compute plan presentation ─────────────────────────────────────────
//
// EVERY VALUE HERE COMES FROM THE SERVER. This file used to carry three
// hardcoded tables — a per-plan-id monthly price, a per-plan-id overage rate,
// and an allowlist that both filtered and ordered the plan grid — which meant
// the price a user read was a second, independent declaration of what a plan
// costs, reconciled with what Stripe charges by nothing at all. The catalog
// (control-plane/internal/plansconfig) now carries price_cents, display_order
// and the overage rate, and ListPlans serves them.
//
// What stays here is FORMATTING: currency symbols, rounding minutes into
// hours, labelling sizes. What left is data. If you find yourself adding a
// `Record<string, number>` keyed by plan id to this file, that is the defect
// this change removed — see __tests__/billingUtils.price.test.ts, which reads
// this source and fails on exactly that shape.

/**
 * `monthlyPriceCents` when the server priced nothing. Distinct from 0, which
 * would render as "$0.00" — a plan we cannot price must be shown as
 * unavailable, never as free. A withheld price is recoverable; a confidently
 * wrong one next to a pay button is not.
 */
export const COMPUTE_PLAN_UNPRICED = null;

export interface PlanDisplay {
  allowedSizes: string[];
  monthlyPriceCents: number | null;
  includedMinutes: number;
  overageCentsPerMinute: number;
}

/** The wire fields this module reads. Narrow on purpose. */
type PlanPricing = Pick<Plan, "priceCents" | "displayOrder" | "structuredLimits">;

export function derivePlanDisplay(
  plan?: PlanPricing | null,
): PlanDisplay {
  const limits = plan?.structuredLimits;
  return {
    allowedSizes: limits?.allowedDaemonSizes ?? [],
    // -1 is the catalog's "unlimited" sentinel and is preserved, not clamped:
    // callers distinguish it from 0 to render "Unlimited".
    includedMinutes: limits?.daemonComputeIncludedMinutes ?? 0,
    overageCentsPerMinute: limits?.daemonOveragePerMinuteCents ?? 0,
    monthlyPriceCents:
      plan && plan.priceCents > 0n
        ? Number(plan.priceCents)
        : COMPUTE_PLAN_UNPRICED,
  };
}

/**
 * Does this plan belong in the purchase grid?
 *
 * Membership is a SERVER fact: a plan the catalog priced is one a user can
 * buy. The previous hardcoded allowlist meant a plan added to the catalog
 * stayed invisible until someone edited the frontend — so the newest tier was
 * always the one nobody could buy.
 */
export function isPurchasableComputePlan(plan: PlanPricing): boolean {
  return plan.priceCents > 0n;
}

/**
 * Is this a compute plan at all, priced or not?
 *
 * Deliberately separate from `isPurchasableComputePlan`, which additionally
 * requires a price. The pair lets a caller tell "this environment sells no
 * compute" from "compute plans exist but arrived unpriced, so none of them can
 * be offered" — two facts that otherwise collapse into one empty grid and one
 * misleading "not configured" message.
 *
 * Keyed on the catalog's product id, which is a server fact, rather than on a
 * plan-id prefix: an id-shaped rule is the same client-side allowlist that
 * previously made every newly added plan invisible until someone edited the
 * frontend.
 */
export function isComputePlan(plan: { productId: string }): boolean {
  return plan.productId === COMPUTE_PRODUCT_ID;
}

/** The catalog's product id for machine compute (control-plane plans.yaml). */
const COMPUTE_PRODUCT_ID = "prod_compute";

/**
 * Order the plan grid by the catalog's `display_order`. Ties and unset orders
 * fall back to price, so a catalog that forgets the field still renders in a
 * sensible order rather than in whatever order the database returned rows.
 */
export function sortPlansForDisplay<T extends PlanPricing>(plans: T[]): T[] {
  return [...plans].sort(
    (a, b) =>
      a.displayOrder - b.displayOrder || Number(a.priceCents - b.priceCents),
  );
}

// ── Machine sizes ─────────────────────────────────────────────────────
//
// Plan and machine size are ONE axis: `PlanLimits.allowed_daemon_sizes` is the
// whole rule, and it is what the backend's `checkDaemonSizeAllowed` enforces.
// Two surfaces ask about it — the purchase grid ("which sizes does this plan
// buy me?") and the create-machine modal ("which may I pick?") — so the rule
// is answered here once instead of being re-derived at each and drifting.
//
// Everything below reads a plan PAYLOAD. Nothing keys off a plan id, for the
// same reason the price tables above had to go: an id-keyed rule is a second
// declaration of something the server already decided.

/**
 * The sizes this client can render, smallest first. Ordering only — it is not
 * an allowlist: a size the catalog offers but this list omits is dropped
 * rather than shown unlabelled, and a size in this list that no plan offers is
 * never presented.
 */
export const DAEMON_SIZE_ORDER = ["small", "medium", "large", "xl"] as const;

export type DaemonSizeName = (typeof DAEMON_SIZE_ORDER)[number];

/** The wire fields the size helpers read. */
type PlanSizes = Pick<Plan, "displayOrder" | "priceCents" | "structuredLimits">;

function allowedSizeSet(plan?: PlanSizes | null): Set<string> {
  return new Set(
    (plan?.structuredLimits?.allowedDaemonSizes ?? []).map((s) =>
      s.toLowerCase(),
    ),
  );
}

/**
 * May a machine of this size run on this plan?
 *
 * Fails CLOSED on an absent plan or absent limits. A permissive default here
 * would offer a size the server then refuses at `CreateDaemon` — the user
 * picks, pays attention, and is told no afterwards.
 */
export function isSizeAllowedByPlan(
  plan: PlanSizes | null | undefined,
  size: DaemonSizeName,
): boolean {
  return allowedSizeSet(plan).has(size.toLowerCase());
}

/** Every plan in the catalog that can run this size. */
export function plansAllowingSize<T extends PlanSizes>(
  plans: T[],
  size: DaemonSizeName,
): T[] {
  return plans.filter((p) => isSizeAllowedByPlan(p, size));
}

/**
 * The cheapest plan that can run this size — the answer to "what do I have to
 * buy for a Medium machine?", which is the question that makes plan and size a
 * single choice rather than two.
 */
export function smallestPlanAllowingSize<T extends PlanSizes>(
  plans: T[],
  size: DaemonSizeName,
): T | undefined {
  return sortPlansForDisplay(plansAllowingSize(plans, size))[0];
}

/**
 * The sizes worth offering at all: the union of what the catalog's plans
 * allow, in size order. A catalog that stops selling XL stops offering XL with
 * no frontend change, and a size name this client cannot label is dropped
 * rather than rendered raw.
 */
export function offeredDaemonSizes(plans: PlanSizes[]): DaemonSizeName[] {
  const offered = new Set<string>();
  for (const plan of plans) {
    for (const size of allowedSizeSet(plan)) offered.add(size);
  }
  return DAEMON_SIZE_ORDER.filter((size) => offered.has(size));
}

const SIZE_DISPLAY_LABELS: Record<string, string> = {
  small: "Small",
  medium: "Medium",
  large: "Large",
  xl: "XL",
};

export function formatSizeLabel(size: string): string {
  return SIZE_DISPLAY_LABELS[size.toLowerCase()] ?? size;
}

export function formatAllowedSizes(sizes: string[]): string {
  if (sizes.length === 0) return "—";
  const labeled = sizes
    .map((s) => SIZE_DISPLAY_LABELS[s.toLowerCase()])
    .filter((label): label is string => !!label);
  if (labeled.length === 4) return "All sizes";
  return labeled.join(", ");
}

export function formatOverageRate(centsPerMinute: number): string {
  if (!(centsPerMinute > 0)) return "—";
  return `$${(centsPerMinute / 100).toFixed(3)}/min`;
}

/**
 * Did this subscription's plan detail fail to reach us?
 *
 * The screenshot's `— / — / 0 h` state: the row ARRIVED and was unusable, and
 * it rendered at full confidence next to a purchase button. Zero and unknown
 * had been collapsed, and the collapse resolved toward the alarming reading.
 *
 * The signature is all three facts absent TOGETHER, which is what a row
 * written before the catalog gained these columns looks like. A plan that
 * genuinely includes no hours still carries sizes and a rate, so requiring
 * all three keeps a real zero-hour plan out of the degraded branch — and that
 * matters, because the degraded branch disables the overage control.
 *
 * Absence of a plan entirely is NOT this state. That is the true empty, and it
 * gets "pick a plan" rather than advice about restarting the control plane.
 */
export function isPlanDetailUnavailable(plan?: PlanPricing | null): boolean {
  if (!plan) return false;
  const d = derivePlanDisplay(plan);
  return (
    d.allowedSizes.length === 0 &&
    d.includedMinutes === 0 &&
    d.overageCentsPerMinute === 0
  );
}

// ── Credit runway ─────────────────────────────────────────────────────
//
// The single most useful thing you can tell someone holding a depleting
// balance, and the thing the page most conspicuously did not say. It is also
// an ESTIMATE ABOUT MONEY, so the rules that WITHHOLD it are load-bearing:
// every branch below that returns null is a number a user would otherwise
// have quoted back at us.

/**
 * Days of observed spend below which no estimate is offered. A one- or
 * two-day window swings wildly — one heavy afternoon halves the reported
 * runway — and the balance above the line already answers the primary
 * question without it.
 */
export const MIN_RUNWAY_SAMPLE_DAYS = 3;

/** Past this, the number is arithmetic rather than information. */
const RUNWAY_CAP_DAYS = 90;

/**
 * How many distinct days the spend sample actually covers.
 *
 * Counting ENTRIES would count one day billed across three models as three
 * days and understate the burn rate threefold — and the burn rate is the
 * denominator of a dollar figure on the page.
 */
export function spendSampleDays(
  entries: { periodStart?: TimestampLike }[],
): number {
  const days = new Set<string>();
  for (const entry of entries) {
    if (!entry.periodStart) continue;
    days.add(
      new Date(Number(entry.periodStart.seconds) * 1000)
        .toISOString()
        .slice(0, 10),
    );
  }
  return days.size;
}

/**
 * Whole days of credit remaining at the observed rate, or null when we should
 * not say. Null is a real answer here and callers must render nothing for it —
 * not "—", not "~0 days".
 */
export function estimateCreditRunwayDays(
  balanceNanos: bigint,
  spendUsd: number,
  sampleDays: number,
): number | null {
  if (balanceNanos <= BigInt(0)) return null;
  if (sampleDays < MIN_RUNWAY_SAMPLE_DAYS) return null;
  if (!(spendUsd > 0)) return null;

  const perDay = spendUsd / sampleDays;
  const days = Math.floor(Number(balanceNanos) / 1_000_000_000 / perDay);
  // Under a day, flooring gives 0 — which reads as a measured value rather
  // than as "about to run out". The low-balance warning speaks instead.
  return days >= 1 ? days : null;
}

export function formatRunway(days: number): string {
  if (days >= RUNWAY_CAP_DAYS) return `~${RUNWAY_CAP_DAYS}+ days at recent use`;
  return `~${days} ${days === 1 ? "day" : "days"} at recent use`;
}

// ── Compute capacity ──────────────────────────────────────────────────

export type ComputeCapacityState =
  | "unlimited"
  | "under"
  | "near"
  | "spent"
  | "overage";

export interface ComputeCapacity {
  /** Fill of the INCLUDED segment, 0–100. Never overflows its own segment. */
  usedPct: number;
  /** Fill of the overage segment past the boundary, 0–100. */
  overagePct: number;
  state: ComputeCapacityState;
}

/** Fraction of the included allowance at which the bar starts warning. */
const NEAR_CEILING_FRACTION = 0.8;

/**
 * Compute's shape: hours used against a ceiling, and how far past it.
 *
 * The overage is a SEPARATE segment beyond the boundary rather than more fill
 * in the same bar — letting used minutes overflow the included segment erases
 * the boundary the whole band exists to make legible.
 */
export function deriveComputeCapacity({
  usedMinutes,
  includedMinutes,
  overageMinutes,
}: {
  usedMinutes: number;
  includedMinutes: number;
  overageMinutes: number;
}): ComputeCapacity {
  // -1 is the catalog's unlimited sentinel. Dividing by it yields a negative
  // percentage and a bar that renders as though the user had overrun.
  if (includedMinutes < 0) {
    return { usedPct: 0, overagePct: 0, state: "unlimited" };
  }
  if (includedMinutes === 0) {
    return {
      usedPct: 0,
      overagePct: overageMinutes > 0 ? 100 : 0,
      state: overageMinutes > 0 ? "overage" : "spent",
    };
  }

  const usedPct = Math.min((usedMinutes / includedMinutes) * 100, 100);
  const overagePct = Math.min((overageMinutes / includedMinutes) * 100, 100);

  const state: ComputeCapacityState =
    overageMinutes > 0
      ? "overage"
      : usedMinutes >= includedMinutes
        ? "spent"
        : usedMinutes >= includedMinutes * NEAR_CEILING_FRACTION
          ? "near"
          : "under";

  return { usedPct, overagePct, state };
}

// ── Dates / invoices ──────────────────────────────────────────────────

interface TimestampLike {
  seconds: bigint | number;
}

export function formatTimestampDate(ts?: TimestampLike): string {
  if (!ts) return "—";
  return new Date(Number(ts.seconds) * 1000).toLocaleDateString("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

export function formatDayLabel(ts?: TimestampLike): string {
  if (!ts) return "—";
  return new Date(Number(ts.seconds) * 1000).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
  });
}

export function formatCentsAsDollars(cents: number | bigint): string {
  return `$${(Number(cents) / 100).toFixed(2)}`;
}

export type InvoiceStatus = "Paid" | "Pending" | "Failed";

export function normalizeInvoiceStatus(status: string): InvoiceStatus {
  const normalized = status.toLowerCase();
  if (normalized === "paid") return "Paid";
  if (["open", "pending", "draft"].includes(normalized)) return "Pending";
  return "Failed";
}

// ── Error helpers ─────────────────────────────────────────────────────

// The global upgradeInterceptor (api/upgradeInterceptor.ts) already opens the
// BillingEmailRequiredModal / UpgradeRequiredModal for these two backend
// signals and re-throws. Call sites use this to avoid ALSO rendering a raw
// inline error banner underneath the modal.
export function isBackendModalError(err: unknown): boolean {
  if (!(err instanceof ConnectError)) return false;
  const reason = err.metadata.get("x-reliant-reason") ?? "";
  if (err.code === Code.InvalidArgument && reason === "billing_email_missing") {
    return true;
  }
  if (err.code === Code.ResourceExhausted && reason) return true;
  return false;
}

export function formatBillingError(err: unknown, fallback: string): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  if (err instanceof Error) return err.message;
  return fallback;
}
