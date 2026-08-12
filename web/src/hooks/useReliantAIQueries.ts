/**
 * TanStack-Query hooks for the managed-Reliant AI settings surface (the
 * "Reliant AI" tab of the `/settings/general` AI section). Mirrors the pattern
 * in `useOnboardingQueries.ts`:
 * thin `useQuery`/`useMutation` wrappers over the `services/controlPlane`
 * async functions, with a shared `['reliantAI', ...]` query-key namespace so
 * mutations can invalidate the reads.
 */

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  getReliantOverview,
  getWalletOverview,
  listLLMKeys,
  listAvailableModels,
  getLLMSpend,
  createManagedLLMKey,
  revokeLLMKey,
  rotateLLMKey,
  redeemCoupon,
  type CreateManagedKeyArgs,
  type GetLLMSpendArgs,
} from "@/services/controlPlane/reliantAI";
import { cloudBillingKeys } from "./useCloudBillingQueries";

const KEY = {
  reliantOverview: ["reliantAI", "reliantOverview"] as const,
  walletOverview: ["reliantAI", "walletOverview"] as const,
  keys: (orgId: string) => ["reliantAI", "keys", orgId] as const,
  models: (orgId: string) => ["reliantAI", "models", orgId] as const,
  spend: (args: GetLLMSpendArgs) =>
    ["reliantAI", "spend", args.orgId, args.startDate, args.endDate] as const,
};

export function useReliantOverview() {
  return useQuery({
    queryKey: KEY.reliantOverview,
    queryFn: () => getReliantOverview(),
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  });
}

export function useWalletOverview() {
  return useQuery({
    queryKey: KEY.walletOverview,
    queryFn: () => getWalletOverview(),
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  });
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
    mutationFn: (code: string) => redeemCoupon(code),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: KEY.walletOverview });
      void queryClient.invalidateQueries({ queryKey: KEY.reliantOverview });
      void queryClient.invalidateQueries({
        queryKey: cloudBillingKeys.computeSubscription,
      });
      void queryClient.invalidateQueries({
        queryKey: cloudBillingKeys.walletOverview,
      });
    },
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