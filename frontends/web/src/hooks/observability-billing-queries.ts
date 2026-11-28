// Copyright (c) 2026 Reliant Labs
import { useQueryClient, type Query } from "@tanstack/react-query";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { billingServiceKeys, useListOrgLimitChangesInfinite } from "@/hooks/controlplane/billing-service-hooks_gen";
import { deployServiceKeys } from "@/hooks/controlplane/deploy-service-hooks_gen";
import {
  observabilityBillingServiceKeys,
  useGetObservabilityLimits,
  useGetObservabilityStorage,
  useGetObservabilityUsage,
  useListObservabilityUsageInfinite,
  useUpdateObservabilityLimits,
} from "@/hooks/controlplane/observability-billing-service-hooks_gen";

/*
 * Every read runs on ObservabilityBillingService's generated hooks (the limit
 * history on BillingService's), which resolve their client from the
 * QueryClient they run on (scopedClient). Their roots are in activeOrgStore's
 * ORG_SCOPED_QUERY_ROOTS, so an org switch drops them.
 */

// Rollout and permissions failures are not transient, and must never turn into zero usage.
export const retryObsBilling = (count: number, error: unknown) => count < 1 && !(error instanceof ConnectError && [Code.Unimplemented, Code.PermissionDenied, Code.InvalidArgument, Code.Unauthenticated].includes(error.code));
/** The limit-change history's request: its infinite key is what a limits save refreshes. */
export const obsLimitHistoryRequest = (orgId: string) => ({ orgId, pageSize: 50 });
/** Whether a cached read's request names this org. */
const readsOrg = (orgId: string) => (query: Query) => (query.queryKey.find((part) => typeof part === "object" && part !== null) as { orgId?: string } | undefined)?.orgId === orgId;

export function useObsBillingUsage(orgId: string | null) {
  return useGetObservabilityUsage({ orgId: orgId ?? "" }, { enabled: !!orgId, retry: retryObsBilling, staleTime: 60_000 });
}
export function useObsBillingLimits(orgId: string) {
  return useGetObservabilityLimits({ orgId }, { enabled: !!orgId, retry: retryObsBilling });
}
export function useObsBillingStorage(orgId: string) {
  return useGetObservabilityStorage({ orgId }, { enabled: !!orgId, retry: retryObsBilling, staleTime: 60_000 });
}
export function useObsBillingDays(orgId: string, month: string) {
  const start = new Date(`${month}-01T00:00:00Z`);
  const end = new Date(Date.UTC(start.getUTCFullYear(), start.getUTCMonth() + 1, 1));
  return useListObservabilityUsageInfinite({ orgId, start: timestampFromDate(start), end: timestampFromDate(end), granularity: 2, groupBy: 2, pageSize: 5000 },
    { enabled: !!orgId, retry: retryObsBilling });
}
export function useObsLimitHistory(orgId: string) {
  return useListOrgLimitChangesInfinite(obsLimitHistoryRequest(orgId), { enabled: !!orgId, retry: retryObsBilling });
}
export function useUpdateObsLimits(orgId: string) {
  const cache = useQueryClient();
  return useUpdateObservabilityLimits({
    // Changing organizations detaches this observer from the old pending write,
    // without cancelling that organization's valid server-side save.
    mutationKey: [...observabilityBillingServiceKeys.all, "updateObservabilityLimits", orgId],
    // Never retry a money-moving write, especially not after Aborted.
    retry: false,
    // The refresh is the saved org's (below), never the whole service's.
    invalidate: false,
    onSuccess: (response, request) => {
      // Mutation options can change while a request is in flight. Its immutable
      // variables, never the current render's organization, own the result.
      const saved = request.orgId ?? "";
      if (response.limits) cache.setQueryData(observabilityBillingServiceKeys.getObservabilityLimits({ orgId: saved }), response.limits);
      if (!saved) return;
      // Every observability-billing read of the org (usage, limits, storage,
      // daily rows), its limit-change history, and its collection policy
      // (ObservabilityCollectionPolicy, on DeployService.ListEnvironments).
      void Promise.all([
        cache.invalidateQueries({ queryKey: observabilityBillingServiceKeys.all, predicate: readsOrg(saved) }),
        cache.invalidateQueries({ queryKey: billingServiceKeys.listOrgLimitChangesInfinite(obsLimitHistoryRequest(saved)) }),
        cache.invalidateQueries({ queryKey: deployServiceKeys.listEnvironments({ orgId: saved }) }),
      ]);
    },
  });
}
