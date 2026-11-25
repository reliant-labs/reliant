// Copyright (c) 2026 Reliant Labs

/**
 * React Query hooks for the ML tab (components/Forge/Ml), over MlApiContext
 * (so tests hand them an in-memory api, and the demo shell its fixture pack).
 *
 * Every key starts with `["ml", orgId]`, so two orgs never share an answer;
 * the "ml" root is org-scoped (store/activeOrgStore ORG_SCOPED_QUERY_ROOTS),
 * so an org switch drops them all. The entitlement is cached for a minute:
 * it changes when Reliant grants or revokes it, and a revocation shows up
 * sooner anyway, as an ML read answering FEATURE_NOT_ENTITLED.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { useRouteOrgIdOverride } from "@/lib/orgScope";
import { currentOrgId } from "@/services/controlPlane/orgs";
import { mlRetry, mlRetryDelay } from "@/services/controlPlane/ml/errors";
import { useMlApi } from "@/services/controlPlane/ml/MlApiContext";
import { ML_FEATURE, type OrgEntitlement } from "@/services/controlPlane/ml/types";

export const mlKeys = {
  /** The page's org when the route names none. */
  org: () => ["ml", "org"] as const,
  all: (orgId: string) => ["ml", orgId] as const,
  entitlements: (orgId: string) => [...mlKeys.all(orgId), "entitlements"] as const,
  machines: (orgId: string) => [...mlKeys.all(orgId), "machines"] as const,
  stack: (orgId: string) => [...mlKeys.all(orgId), "stack"] as const,
  findings: (orgId: string) => [...mlKeys.all(orgId), "findings"] as const,
  runs: (orgId: string, project: string) => [...mlKeys.all(orgId), "runs", project] as const,
  models: (orgId: string, project: string) => [...mlKeys.all(orgId), "models", project] as const,
  inference: (orgId: string, project: string) => [...mlKeys.all(orgId), "inference", project] as const,
};

const READ = { retry: mlRetry, retryDelay: mlRetryDelay, staleTime: 30_000 } as const;

/**
 * The org every ML request names (they take a REQUIRED org_id). On an org
 * page it is the route's org (lib/orgScope.ts); outside the org tree (the
 * flag off, the demo shell) the tab's org, read once.
 */
export function useMlOrg() {
  const routeOrg = useRouteOrgIdOverride();
  const org = useQuery({ queryKey: mlKeys.org(), queryFn: () => currentOrgId(), staleTime: 5 * 60_000, enabled: routeOrg === undefined });
  if (routeOrg !== undefined) return { orgId: routeOrg, isLoading: routeOrg === null, error: null as unknown, refetch: org.refetch };
  return { orgId: org.data ?? null, isLoading: org.isLoading, error: org.error as unknown, refetch: org.refetch };
}

/** The org's entitlement to `ml`: what the gate predicts from (the server still decides). */
export function useMlEntitlement(orgId: string | null) {
  const api = useMlApi();
  return useQuery({
    queryKey: mlKeys.entitlements(orgId ?? ""),
    queryFn: ({ signal }) => api.getMyOrgEntitlements(orgId!, { signal }),
    enabled: !!orgId,
    retry: mlRetry,
    retryDelay: mlRetryDelay,
    staleTime: 60_000,
    select: mlEntitlementOf,
  });
}

/** The `ml` row of an entitlement list; a list without it is "not enabled, never requested". */
export function mlEntitlementOf(features: OrgEntitlement[]): OrgEntitlement {
  return features.find((f) => f.feature === ML_FEATURE) ?? { feature: ML_FEATURE, enabled: false, requestedAtMs: null, grantedAtMs: null };
}

/**
 * Request access to ML. The server records it once per org; its answer
 * replaces the cached entitlement, so the page shows "Requested on …" from
 * what the server stored. An org that turns out to have it already re-reads
 * the entitlement, and the pages open.
 */
export function useRequestMlAccess(orgId: string | null) {
  const api = useMlApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (note?: string) => api.requestOrgEntitlement(orgId!, ML_FEATURE, note),
    onSuccess: (result) => {
      if (!orgId) return;
      const key = mlKeys.entitlements(orgId);
      qc.setQueryData<OrgEntitlement[]>(key, (prev = []) => [...prev.filter((f) => f.feature !== ML_FEATURE), result.entitlement]);
      if (result.alreadyEnabled) void qc.invalidateQueries({ queryKey: mlKeys.all(orgId) });
    },
  });
}

export function useMlMachines(orgId: string) {
  const api = useMlApi();
  return useQuery({ queryKey: mlKeys.machines(orgId), queryFn: ({ signal }) => api.getMachines({ orgId }, { signal }), ...READ });
}

export function useMlStack(orgId: string) {
  const api = useMlApi();
  return useQuery({ queryKey: mlKeys.stack(orgId), queryFn: ({ signal }) => api.getStack({ orgId }, { signal }), ...READ });
}

export function useMlFindings(orgId: string) {
  const api = useMlApi();
  return useQuery({ queryKey: mlKeys.findings(orgId), queryFn: ({ signal }) => api.listFindings({ orgId }, { signal }), ...READ });
}

const projectReq = (orgId: string, project: string) => (project ? { orgId, project } : { orgId });

export function useMlRuns(orgId: string, project = "") {
  const api = useMlApi();
  return useQuery({ queryKey: mlKeys.runs(orgId, project), queryFn: ({ signal }) => api.listRuns(projectReq(orgId, project), { signal }), ...READ });
}

export function useMlModels(orgId: string, project = "") {
  const api = useMlApi();
  return useQuery({ queryKey: mlKeys.models(orgId, project), queryFn: ({ signal }) => api.listModels(projectReq(orgId, project), { signal }), ...READ });
}

export function useMlInference(orgId: string, project = "") {
  const api = useMlApi();
  return useQuery({
    queryKey: mlKeys.inference(orgId, project),
    queryFn: ({ signal }) => api.listInferenceServices(projectReq(orgId, project), { signal }),
    ...READ,
  });
}
