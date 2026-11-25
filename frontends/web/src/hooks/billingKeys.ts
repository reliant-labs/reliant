// Copyright (c) 2026 Reliant Labs

/**
 * Cache scopes shared by the BillingService reads and writes, on the
 * generated keys (hooks/controlplane/billing-service-hooks_gen.ts).
 *
 * A generated write invalidates the whole service unless told otherwise, so
 * every billing write passes `invalidate: refreshing(...)` naming exactly the
 * reads it changes.
 */

import type { QueryClient, QueryKey } from "@tanstack/react-query";

import { billingServiceKeys } from "@/hooks/controlplane/billing-service-hooks_gen";

/** A generated write's `invalidate`: refetch exactly these scopes. */
export function refreshing(...keys: readonly QueryKey[]) {
  return (queryClient: QueryClient) => {
    for (const queryKey of keys) void queryClient.invalidateQueries({ queryKey });
  };
}

/**
 * Every read of the in-app billing dashboard (useCloudBillingQueries), each
 * across every org and request: what the `["cloud-billing"]` family held.
 */
export const CLOUD_BILLING_READS: readonly QueryKey[] = [
  billingServiceKeys.getCurrentUserComputeSubscription.all,
  billingServiceKeys.getCurrentUserWalletOverview.all,
  billingServiceKeys.getCurrentUserComputeUsage.all,
  billingServiceKeys.listPlans.all,
  billingServiceKeys.listCurrentUserInvoices.all,
  billingServiceKeys.getCurrentUserBillingEmail.all,
  billingServiceKeys.getCurrentUserWalletTopupQuote.all,
  billingServiceKeys.getCurrentUserWalletAutoRecharge.all,
];
