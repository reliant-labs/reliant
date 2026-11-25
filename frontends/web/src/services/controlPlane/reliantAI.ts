/**
 * The managed-Reliant AI surface's UI types and availability flag:
 *   - `controlplane.v1.LLMGatewayService` (LLM keys, spend, available models)
 *   - `controlplane.v1.BillingService`     (RedeemCoupon's answer, mapped)
 *
 * These back the "Reliant AI" tab of Billing (`ReliantAISection`), the
 * end-user view of Reliant-issued LLM keys + spend. Distinct from the BYO
 * provider accounts in Settings → AI → Providers. The calls themselves run on
 * the generated hooks, through hooks/useReliantAIQueries.ts.
 */

import {
  RedeemedCouponKind,
  type RedeemCouponResponse,
} from "@/gen/controlplane/services/billing/v1/billing_pb";
import type {
  LLMKey,
  LLMSpendEntry,
  WalletOverview,
  ReliantOverview,
} from "@/gen/controlplane/controlplane/v1/shared_pb";
import { hasControlPlane } from "./config";

export type { LLMKey, LLMSpendEntry, WalletOverview, ReliantOverview };
export { RedeemedCouponKind };

/**
 * Whether the managed-AI surface is wired up in this build. `ReliantAISection`
 * reads this to render a graceful "cloud not configured" state instead of
 * calling a control plane that is not there. Components must consume the flag
 * from a service module (never import `config.hasControlPlane` directly).
 */
export const reliantAIAvailable = hasControlPlane;

export interface CreateManagedKeyArgs {
  orgId: string;
  name: string;
  models: string[];
  /** Optional monthly/period budget cap in USD. Omit for no limit. */
  maxBudget?: number;
  /** e.g. "30d" — pairs with maxBudget. Empty string = server default. */
  budgetDuration?: string;
}

/** A created or rotated key, with its one-time plaintext secret. */
export interface IssuedLLMKey {
  key?: LLMKey;
  plaintextKey: string;
}

export interface GetLLMSpendArgs {
  orgId: string;
  startDate: string;
  endDate: string;
  keyId?: string;
}

/** GetLLMSpend's answer: per-key/per-model spend entries + total for a date range. */
export interface LLMSpend {
  entries: LLMSpendEntry[];
  totalSpend: number;
  /**
   * Distinct calendar days the spend was observed over, per the server.
   *
   * Forwarded rather than derived. `entries` are aggregated per (key, model)
   * across the whole range and carry no period, so the credit band's burn rate
   * has no denominator without this — which is why its runway estimate
   * silently never rendered before the server started sending it. Zero means
   * "cannot say", and callers must withhold rather than substitute.
   */
  sampleDays: number;
}

/** Result of redeeming a coupon code. */
export interface RedeemCouponResult {
  /** WALLET_CREDIT only: credit added to the org wallet, in cents. */
  amountCents: number;
  /** The code as stored server-side (uppercase). */
  code: string;
  /** WALLET_CREDIT only: wallet balance after the credit, in cents. */
  newBalanceCents: number;
  /** Which fields above/below are meaningful. */
  kind: RedeemedCouponKind;
  /** COMPUTE_MINUTES only: daemon compute minutes granted by this redemption. */
  computeMinutes: number;
  /** COMPUTE_MINUTES only: org's total unspent grant balance after this redemption, in small-daemon-seconds. */
  newGrantedSmallDaemonSecondsRemaining: number;
}

/**
 * RedeemCoupon's answer in UI units — credit the current org's wallet, or
 * grant daemon compute minutes, from a coupon code (see `kind`).
 *
 * The org is resolved SERVER-side from the authenticated caller; the request
 * deliberately has no org, so a client cannot credit someone else's wallet.
 * Redemption is idempotent per (coupon, org): redeeming twice returns an
 * AlreadyExists error rather than crediting again.
 */
export function redeemCouponResult(res: RedeemCouponResponse): RedeemCouponResult {
  return {
    // Wire values are bigint (int64); the UI works in numbers. Coupon amounts
    // are dollars, so this is nowhere near Number's safe range.
    amountCents: Number(res.amountCents),
    code: res.code,
    newBalanceCents: Number(res.newBalanceCents),
    kind: res.kind,
    computeMinutes: Number(res.computeMinutes),
    newGrantedSmallDaemonSecondsRemaining: Number(res.newGrantedSmallDaemonSecondsRemaining),
  };
}
