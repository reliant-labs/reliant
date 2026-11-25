/**
 * TanStack-Query hooks for the managed-Reliant AI settings surface (the
 * "Reliant AI" tab of Billing): thin wrappers over the generated BillingService
 * and LLMGatewayService hooks (hooks/controlplane/), on their generated keys,
 * that keep the shapes the UI reads.
 */

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import type {
  GetCurrentUserReliantOverviewResponse,
  GetCurrentUserWalletOverviewResponse,
} from "@/gen/controlplane/services/billing/v1/billing_pb";
import { billingServiceKeys, billingServiceMutations, billingServiceQueries } from "@/hooks/controlplane/billing-service-hooks_gen";
import type {
  GetLLMSpendResponse,
  ListAvailableModelsResponse,
  ListLLMKeysResponse,
} from "@/gen/controlplane/services/llm_gateway/v1/llm_gateway_pb";
import {
  llmGatewayServiceKeys,
  llmGatewayServiceMutations,
  useGetLLMSpend,
  useListAvailableModels,
  useListLLMKeys,
} from "@/hooks/controlplane/llm-gateway-service-hooks_gen";
import { refreshing } from "@/hooks/billingKeys";
import {
  redeemCouponResult,
  type CreateManagedKeyArgs,
  type GetLLMSpendArgs,
  type IssuedLLMKey,
  type LLMKey,
  type LLMSpend,
} from "@/services/controlPlane/reliantAI";
import { orgQueryEnabled, orgRequest, useOrgScope, withOrgFailure } from "@/lib/orgScope";

const reliantOverviewOf = (res: GetCurrentUserReliantOverviewResponse) => res.overview;
const walletOverviewOf = (res: GetCurrentUserWalletOverviewResponse) => res.overview;

/** In the tab's org: read in it, keyed by it, and not before it is known (lib/orgScope). Entitlement + period spend/remaining/cap. */
export function useReliantOverview() {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      billingServiceQueries.getCurrentUserReliantOverview(orgRequest({}, org.orgId), {
        enabled: orgQueryEnabled(org),
        staleTime: 30_000,
        refetchOnWindowFocus: false,
        select: reliantOverviewOf,
      }),
      org,
    ),
  );
}

/**
 * In the tab's org, like useReliantOverview: the wallet balance and its ten
 * newest ledger entries. (The billing dashboard's useWalletOverview reads five,
 * so it is a different request and cache entry.)
 */
export function useWalletOverview() {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      billingServiceQueries.getCurrentUserWalletOverview(orgRequest({ ledgerPage: 1, ledgerPageSize: 10 }, org.orgId), {
        enabled: orgQueryEnabled(org),
        staleTime: 30_000,
        refetchOnWindowFocus: false,
        select: walletOverviewOf,
      }),
      org,
    ),
  );
}

/**
 * Redeem a coupon code — either wallet credit or daemon compute minutes,
 * per the response's `kind` (see RedeemCouponResult).
 *
 * Invalidates both the wallet/Reliant-overview reads AND the compute
 * subscription read on success. The response's kind isn't known until after
 * the call, and one redeem box serves both coupon kinds, so it's simplest
 * (and harmless) to invalidate both sets unconditionally rather than branch
 * on `kind` here.
 *
 * Errors are surfaced to the CALLER rather than toasted here: the distinct
 * failures (unknown code / already redeemed / fully claimed / expired) each
 * need their own message next to the input, not a generic toast.
 */
export function useRedeemCoupon() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (code: string) =>
      redeemCouponResult(
        await billingServiceMutations(queryClient).redeemCoupon(
          { code },
          {
            invalidate: refreshing(
              // Every wallet overview (this tab's and the billing dashboard's).
              billingServiceKeys.getCurrentUserWalletOverview.all,
              billingServiceKeys.getCurrentUserReliantOverview.all,
              billingServiceKeys.getCurrentUserComputeSubscription.all,
              // The read that actually GATES compute, and the one this list was
              // missing. A compute coupon grants MINUTES, not a subscription, so
              // invalidating the subscription key above refreshes a row the coupon did
              // not touch while leaving the eligibility answer — which folds granted
              // minutes in — stale for its full 30s staleTime.
              //
              // It belongs here rather than at the call sites. Onboarding's compute
              // step happened to refetch eligibility by hand, so the gap was invisible
              // there and load-bearing everywhere else: a code redeemed on the billing
              // page granted minutes server-side while every eligibility-driven
              // surface went on believing the user had none.
              billingServiceKeys.getCurrentUserComputeEligibility.all,
            ),
          },
        ),
      ),
  });
}

// ─── LLMGatewayService: Reliant-managed keys, spend, available models ───
//
// Reads and writes on the generated hooks (hooks/controlplane/
// llm-gateway-service-hooks_gen.ts). These wrappers keep the UI's shapes:
// each read maps its response in `select`, and each write keeps its call
// signature so `mutate(keyId)` and `variables` mean what they did.

const keysOf = (res: ListLLMKeysResponse): LLMKey[] => res.keys;
const modelsOf = (res: ListAvailableModelsResponse): string[] => res.models;
const spendOf = (res: GetLLMSpendResponse): LLMSpend => ({
  entries: res.entries,
  totalSpend: res.totalSpend,
  sampleDays: res.sampleDays,
});

export function useLLMKeys(orgId: string) {
  return useListLLMKeys({ orgId }, { enabled: !!orgId, staleTime: 15_000, select: keysOf });
}

export function useAvailableModels(orgId: string, enabled = true) {
  return useListAvailableModels({ orgId }, { enabled: !!orgId && enabled, staleTime: 60_000, select: modelsOf });
}

export function useLLMSpend(args: GetLLMSpendArgs) {
  return useGetLLMSpend(
    { orgId: args.orgId, startDate: args.startDate, endDate: args.endDate, keyId: args.keyId },
    { enabled: !!args.orgId, staleTime: 30_000, refetchOnWindowFocus: false, select: spendOf },
  );
}

/**
 * The reads a key write changes: `orgId`'s key list and every spend window of
 * `orgId`. `getLLMSpend({ orgId })` is a prefix of each of the org's spend
 * keys, because React Query matches a key's request object partially. With no
 * org there is nothing to refresh (both reads are disabled without one), and
 * an empty request would match every org's spend.
 */
function orgKeyReads(orgId: string) {
  if (!orgId) return false as const;
  return refreshing(llmGatewayServiceKeys.listLLMKeys({ orgId }), llmGatewayServiceKeys.getLLMSpend({ orgId }));
}

/**
 * CreateLLMKey: provisions a managed key and returns the one-time plaintext
 * secret. The caller MUST surface `plaintextKey` immediately; it is never
 * retrievable again.
 */
export function useCreateLLMKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (args: CreateManagedKeyArgs): Promise<IssuedLLMKey> => {
      // An empty org could be filled from a later selection after async auth.
      if (!args.orgId) throw new Error("Choose an organization before creating a key.");
      const res = await llmGatewayServiceMutations(queryClient).createLLMKey(
        {
          orgId: args.orgId,
          name: args.name,
          models: args.models,
          maxBudget: args.maxBudget,
          budgetDuration: args.budgetDuration ?? "",
        },
        { invalidate: orgKeyReads(args.orgId) },
      );
      return { key: res.key, plaintextKey: res.plaintextKey };
    },
  });
}

/**
 * RevokeLLMKey: permanently disables a key. The immutable key id pins its org
 * (the server checks permission against the key's org), so `orgId` only names
 * the reads to refresh.
 */
export function useRevokeLLMKey(orgId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (keyId: string): Promise<void> => {
      await llmGatewayServiceMutations(queryClient).revokeLLMKey({ keyId }, { invalidate: orgKeyReads(orgId) });
    },
  });
}

/**
 * RotateLLMKey: issues a fresh secret for an existing key. `gracePeriod`
 * (e.g. "24h") keeps the old secret valid during cut-over; empty = immediate.
 * Resource-bound like revoke.
 */
export function useRotateLLMKey(orgId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (vars: { keyId: string; gracePeriod?: string }): Promise<IssuedLLMKey> => {
      const res = await llmGatewayServiceMutations(queryClient).rotateLLMKey(
        { keyId: vars.keyId, gracePeriod: vars.gracePeriod ?? "" },
        { invalidate: orgKeyReads(orgId) },
      );
      return { key: res.key, plaintextKey: res.plaintextKey };
    },
  });
}