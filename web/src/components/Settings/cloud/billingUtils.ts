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
// Source of truth per field:
//   - allowedSizes / includedMinutes / overageCentsPerMinute come straight
//     out of the plan.limits JSON blob (db/migrations 00054_seed_compute_plans).
//   - monthlyPriceCents is NOT yet on the proto Plan nor in the seed JSON,
//     so the per-tier prices live in COMPUTE_PLAN_PRICE_CENTS below.
//   - overageCentsPerMinute has a per-tier fallback for the same reason.
// Keeping the tables here (not duplicated across tabs) means a future
// migration that adds these to the limits blob only flips this helper.

export const COMPUTE_PLAN_IDS = [
  "plan_compute_small",
  "plan_compute_medium",
  "plan_compute_large",
  "plan_compute_xl",
] as const;

const COMPUTE_PLAN_PRICE_CENTS: Record<string, number> = {
  plan_compute_small: 2000,
  plan_compute_medium: 4000,
  plan_compute_large: 8000,
  plan_compute_xl: 16000,
};

const COMPUTE_PLAN_OVERAGE_FALLBACK_CENTS_PER_MIN: Record<string, number> = {
  plan_compute_small: 0.2,
  plan_compute_medium: 0.4,
  plan_compute_large: 0.8,
  plan_compute_xl: 1.6,
};

export interface PlanDisplay {
  allowedSizes: string[];
  monthlyPriceCents: number | null;
  includedMinutes: number;
  overageCentsPerMinute: number;
}

function parsePlanLimits(raw?: string): Record<string, unknown> | null {
  if (!raw) return null;
  try {
    return JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return null;
  }
}

export function derivePlanDisplay(
  plan?: Pick<Plan, "id" | "limits"> | null,
): PlanDisplay {
  if (!plan) {
    return {
      allowedSizes: [],
      monthlyPriceCents: null,
      includedMinutes: 0,
      overageCentsPerMinute: 0,
    };
  }
  const limits = parsePlanLimits(plan.limits);

  const rawSizes = limits?.allowed_daemon_sizes;
  const allowedSizes = Array.isArray(rawSizes)
    ? rawSizes.filter((s): s is string => typeof s === "string")
    : [];

  const includedMinutes =
    typeof limits?.daemon_compute_included_minutes === "number"
      ? (limits.daemon_compute_included_minutes as number)
      : 0;

  const overageFromLimits =
    typeof limits?.daemon_overage_per_minute_cents === "number"
      ? (limits.daemon_overage_per_minute_cents as number)
      : null;
  const overageCentsPerMinute =
    overageFromLimits !== null
      ? overageFromLimits
      : (COMPUTE_PLAN_OVERAGE_FALLBACK_CENTS_PER_MIN[plan.id] ?? 0);

  const monthlyPriceCents = COMPUTE_PLAN_PRICE_CENTS[plan.id] ?? null;

  return { allowedSizes, monthlyPriceCents, includedMinutes, overageCentsPerMinute };
}

const SIZE_DISPLAY_LABELS: Record<string, string> = {
  small: "Small",
  medium: "Medium",
  large: "Large",
  xl: "XL",
};

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
