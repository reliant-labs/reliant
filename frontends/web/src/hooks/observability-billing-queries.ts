// Copyright (c) 2026 Reliant Labs
import { createContext, useContext } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, type Client } from "@connectrpc/connect";
import { ObservabilityBillingService } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";

export const ObservabilityBillingApiContext = createContext<Client<typeof ObservabilityBillingService> | null>(null);
export function useObservabilityBillingApi() {
  // Defer transport creation until a query runs: local-only installs and the
  // no-org state must render unavailable rather than throw during render.
  const override = useContext(ObservabilityBillingApiContext);
  const api: Client<typeof ObservabilityBillingService> = override ?? {
    getObservabilityUsage: (request, options) => getControlPlaneClient(ObservabilityBillingService).getObservabilityUsage(request, options),
    listObservabilityUsage: (request, options) => getControlPlaneClient(ObservabilityBillingService).listObservabilityUsage(request, options),
    getObservabilityStorage: (request, options) => getControlPlaneClient(ObservabilityBillingService).getObservabilityStorage(request, options),
    getObservabilityLimits: (request, options) => getControlPlaneClient(ObservabilityBillingService).getObservabilityLimits(request, options),
    updateObservabilityLimits: (request, options) => getControlPlaneClient(ObservabilityBillingService).updateObservabilityLimits(request, options),
  };
  return api;
}
export const obsBillingKey = (orgId: string | null) => ["observability-billing", orgId] as const;
// Rollout and permissions failures are not transient, and must never turn into zero usage.
export const retryObsBilling = (count: number, error: unknown) => count < 1 && !(error instanceof ConnectError && [Code.Unimplemented, Code.PermissionDenied, Code.InvalidArgument, Code.Unauthenticated].includes(error.code));
export function useObsBillingUsage(orgId: string | null) {
  const api = useObservabilityBillingApi();
  return useQuery({ queryKey: [...obsBillingKey(orgId), "usage"], enabled: !!orgId,
    queryFn: () => api.getObservabilityUsage({ orgId: orgId! }), retry: retryObsBilling, staleTime: 60_000 });
}
export function useObsBillingLimits(orgId: string) {
  const api = useObservabilityBillingApi();
  return useQuery({ queryKey: [...obsBillingKey(orgId), "limits"], queryFn: () => api.getObservabilityLimits({ orgId }), retry: retryObsBilling });
}
export function useObsBillingStorage(orgId: string) {
  const api = useObservabilityBillingApi();
  return useQuery({ queryKey: [...obsBillingKey(orgId), "storage"], queryFn: () => api.getObservabilityStorage({ orgId }), retry: retryObsBilling, staleTime: 60_000 });
}
export function useObsBillingDays(orgId: string, month: string) {
  const api = useObservabilityBillingApi();
  const start = new Date(`${month}-01T00:00:00Z`);
  const end = new Date(Date.UTC(start.getUTCFullYear(), start.getUTCMonth() + 1, 1));
  return useInfiniteQuery({ queryKey: [...obsBillingKey(orgId), "days", month], initialPageParam: "",
    queryFn: ({ pageParam }) => api.listObservabilityUsage({ orgId, start: timestampFromDate(start), end: timestampFromDate(end), granularity: 2, groupBy: 2, pageSize: 5000, pageToken: pageParam }),
    getNextPageParam: (last) => last.nextPageToken || undefined, retry: retryObsBilling });
}
export function useObsLimitHistory(orgId: string) {
  return useInfiniteQuery({ queryKey: [...obsBillingKey(orgId), "history"], initialPageParam: "",
    queryFn: ({ pageParam }) => getControlPlaneClient(BillingService).listOrgLimitChanges({ orgId, pageSize: 50, pageToken: pageParam }),
    getNextPageParam: (last) => last.nextPageToken || undefined, retry: retryObsBilling });
}
export function useUpdateObsLimits(orgId: string) {
  const api = useObservabilityBillingApi();
  const cache = useQueryClient();
  return useMutation({
    // Changing organizations detaches this observer from the old pending write,
    // without cancelling that organization's valid server-side save.
    mutationKey: [...obsBillingKey(orgId), "update-limits"],
    mutationFn: (request: Parameters<typeof api.updateObservabilityLimits>[0]) => api.updateObservabilityLimits(request),
    // Never retry a money-moving write, especially not after Aborted.
    retry: false,
    onSuccess: async (response, request) => {
      // Mutation options can change while a request is in flight. Its immutable
      // variables, never the current render's organization, own the result.
      const key = obsBillingKey(request.orgId ?? null);
      if (response.limits) cache.setQueryData([...key, "limits"], response.limits);
      await cache.invalidateQueries({ queryKey: key });
    } });
}
