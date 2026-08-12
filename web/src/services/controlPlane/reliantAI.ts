/**
 * Cloud-only wrappers around the managed-Reliant AI surface:
 *   - `controlplane.v1.LLMGatewayService` (LLM keys, spend, available models)
 *   - `controlplane.v1.BillingService`     (wallet + Reliant overview)
 *
 * These back the "Reliant AI" tab of the `/settings/general` AI section
 * (`ReliantAISection`), which is the end-user view of Reliant-issued LLM keys +
 * spend. Distinct from the BYO provider-key flow on the "Your providers" tab of
 * the same section.
 *
 * New file (per the multi-agent contract) — the existing `llm.ts` wrapper is
 * shared with onboarding and left untouched. Every call goes through the shared
 * `getControlPlaneClient` transport (auth + tracing + upgrade-modal chain).
 */

import { LLMGatewayService } from "@/gen/controlplane/v1/public/llm_gateway_service_pb";
import {
  BillingService,
  RedeemedCouponKind,
} from "@/gen/controlplane/v1/public/billing_service_pb";
import type {
  LLMKey,
  LLMSpendEntry,
  WalletOverview,
  ReliantOverview,
} from "@/gen/controlplane/v1/public/shared_pb";
import { getControlPlaneClient } from "./client";
import { hasControlPlane } from "./config";

export type { LLMKey, LLMSpendEntry, WalletOverview, ReliantOverview };
export { RedeemedCouponKind };

/**
 * Whether the managed-AI surface is wired up in this build. `ReliantAISection`
 * reads this to render a graceful "cloud not configured" state instead of
 * letting `getControlPlaneClient` throw. Components must consume the flag from
 * a service module (never import `config.hasControlPlane` directly).
 */
export const reliantAIAvailable = hasControlPlane;

/** GetCurrentUserReliantOverview — entitlement + period spend/remaining/cap. */
export async function getReliantOverview(): Promise<ReliantOverview | undefined> {
  const res = await getControlPlaneClient(
    BillingService,
  ).getCurrentUserReliantOverview({});
  return res.overview;
}

/** GetCurrentUserWalletOverview — wallet balance + recent ledger entries. */
export async function getWalletOverview(): Promise<WalletOverview | undefined> {
  const res = await getControlPlaneClient(
    BillingService,
  ).getCurrentUserWalletOverview({ ledgerPage: 1, ledgerPageSize: 10 });
  return res.overview;
}

/** ListLLMKeys — Reliant-managed keys for the resolved org. */
export async function listLLMKeys(orgId: string): Promise<LLMKey[]> {
  const res = await getControlPlaneClient(LLMGatewayService).listLLMKeys({
    orgId,
  });
  return res.keys;
}

/** ListAvailableModels — models the org's plan permits (drives key create). */
export async function listAvailableModels(orgId: string): Promise<string[]> {
  const res = await getControlPlaneClient(LLMGatewayService).listAvailableModels(
    { orgId },
  );
  return res.models;
}

export interface CreateManagedKeyArgs {
  orgId: string;
  name: string;
  models: string[];
  /** Optional monthly/period budget cap in USD. Omit for no limit. */
  maxBudget?: number;
  /** e.g. "30d" — pairs with maxBudget. Empty string = server default. */
  budgetDuration?: string;
}

/**
 * CreateLLMKey — provisions a managed key and returns the one-time plaintext
 * secret. The caller MUST surface `plaintextKey` immediately; it is never
 * retrievable again.
 */
export async function createManagedLLMKey(
  args: CreateManagedKeyArgs,
): Promise<{ key?: LLMKey; plaintextKey: string }> {
  const res = await getControlPlaneClient(LLMGatewayService).createLLMKey({
    orgId: args.orgId,
    name: args.name,
    models: args.models,
    maxBudget: args.maxBudget,
    budgetDuration: args.budgetDuration ?? "",
  });
  return { key: res.key, plaintextKey: res.plaintextKey };
}

/** RevokeLLMKey — permanently disables a key. */
export async function revokeLLMKey(keyId: string): Promise<void> {
  await getControlPlaneClient(LLMGatewayService).revokeLLMKey({ keyId });
}

/**
 * RotateLLMKey — issues a fresh secret for an existing key. `gracePeriod`
 * (e.g. "24h") keeps the old secret valid during cut-over; empty = immediate.
 * Returns the new one-time plaintext secret.
 */
export async function rotateLLMKey(
  keyId: string,
  gracePeriod = "",
): Promise<{ key?: LLMKey; plaintextKey: string }> {
  const res = await getControlPlaneClient(LLMGatewayService).rotateLLMKey({
    keyId,
    gracePeriod,
  });
  return { key: res.key, plaintextKey: res.plaintextKey };
}

export interface GetLLMSpendArgs {
  orgId: string;
  startDate: string;
  endDate: string;
  keyId?: string;
}

/** GetLLMSpend — per-key/per-model spend entries + total for a date range. */
export async function getLLMSpend(
  args: GetLLMSpendArgs,
): Promise<{ entries: LLMSpendEntry[]; totalSpend: number }> {
  const res = await getControlPlaneClient(LLMGatewayService).getLLMSpend({
    orgId: args.orgId,
    startDate: args.startDate,
    endDate: args.endDate,
    keyId: args.keyId,
  });
  return { entries: res.entries, totalSpend: res.totalSpend };
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
  /** COMPUTE_MINUTES only: org's total unspent granted compute minutes after this redemption. */
  newComputeMinutesRemaining: number;
}

/**
 * RedeemCoupon — credit the current org's wallet, or grant daemon compute
 * minutes, from a coupon code (see `kind` on the result).
 *
 * The org is resolved SERVER-side from the authenticated caller; there is
 * deliberately no org parameter here, so a client cannot credit someone else's
 * wallet. Redemption is idempotent per (coupon, org): redeeming twice returns
 * an AlreadyExists error rather than crediting again.
 */
export async function redeemCoupon(code: string): Promise<RedeemCouponResult> {
  const res = await getControlPlaneClient(BillingService).redeemCoupon({ code });
  return {
    // Wire values are bigint (int64); the UI works in numbers. Coupon amounts
    // are dollars, so this is nowhere near Number's safe range.
    amountCents: Number(res.amountCents),
    code: res.code,
    newBalanceCents: Number(res.newBalanceCents),
    kind: res.kind,
    computeMinutes: Number(res.computeMinutes),
    newComputeMinutesRemaining: Number(res.newComputeMinutesRemaining),
  };
}
