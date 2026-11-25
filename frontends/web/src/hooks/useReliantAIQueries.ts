/**
 * TanStack-Query hooks for the managed-Reliant AI settings surface (the
 * "Reliant AI" tab of Billing). Mirrors the pattern
 * in `useOnboardingQueries.ts`:
 * thin `useQuery`/`useMutation` wrappers over the `services/controlPlane`
 * async functions, with a shared `['reliantAI', ...]` query-key namespace so
 * mutations can invalidate the reads.
 */

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import type {
  GetCurrentUserReliantOverviewResponse,
  GetCurrentUserWalletOverviewResponse,
} from "@/gen/controlplane/services/billing/v1/billing_pb";
import { billingServiceKeys, billingServiceMutations, billingServiceQueries } from "@/hooks/controlplane/billing-service-hooks_gen";
import { refreshing } from "@/hooks/billingKeys";
import {
  listLLMKeys,
  listAvailableModels,
  getLLMSpend,
  createManagedLLMKey,
  revokeLLMKey,
  rotateLLMKey,
  redeemCouponResult,
  type CreateManagedKeyArgs,
  type GetLLMSpendArgs,
} from "@/services/controlPlane/reliantAI";
import { orgQueryEnabled, orgRequest, useOrgScope, withOrgFailure } from "@/lib/orgScope";

// The BillingService reads (overviews) are on the generated keys; these are
// LLMGatewayService's.
const KEY = {
  keys: (orgId: string) => ["reliantAI", "keys", orgId] as const,
  models: (orgId: string) => ["reliantAI", "models", orgId] as const,
  spend: (args: GetLLMSpendArgs) =>
    ["reliantAI", "spend", args.orgId, args.startDate, args.endDate] as const,
};

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

export function useLLMKeys(orgId: string) {
  return useQuery({
    queryKey: KEY.keys(orgId),
    queryFn: () => listLLMKeys(orgId),
    enabled: !!orgId,
    staleTime: 15_000,
  });
}

export function useAvailableModels(orgId: string, enabled = true) {
  return useQuery({
    queryKey: KEY.models(orgId),
    queryFn: () => listAvailableModels(orgId),
    enabled: !!orgId && enabled,
    staleTime: 60_000,
  });
}

export function useLLMSpend(args: GetLLMSpendArgs) {
  return useQuery({
    queryKey: KEY.spend(args),
    queryFn: () => getLLMSpend(args),
    enabled: !!args.orgId,
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  });
}

/**
 * Invalidates every read scoped to `orgId` (keys + spend). Used by all three
 * key mutations so the list/spend cards refresh after create/revoke/rotate.
 */
function useInvalidateOrg() {
  const qc = useQueryClient();
  return (orgId: string) => {
    qc.invalidateQueries({ queryKey: KEY.keys(orgId) });
    qc.invalidateQueries({ queryKey: ["reliantAI", "spend", orgId] });
  };
}

export function useCreateLLMKey() {
  const invalidate = useInvalidateOrg();
  return useMutation({
    mutationFn: (args: CreateManagedKeyArgs) => createManagedLLMKey(args),
    onSuccess: (_data, vars) => invalidate(vars.orgId),
  });
}

export function useRevokeLLMKey(orgId: string) {
  const invalidate = useInvalidateOrg();
  return useMutation({
    mutationFn: (keyId: string) => revokeLLMKey(keyId),
    onSuccess: () => invalidate(orgId),
  });
}

export function useRotateLLMKey(orgId: string) {
  const invalidate = useInvalidateOrg();
  return useMutation({
    mutationFn: (vars: { keyId: string; gracePeriod?: string }) =>
      rotateLLMKey(vars.keyId, vars.gracePeriod ?? ""),
    onSuccess: () => invalidate(orgId),
  });
}