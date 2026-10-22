// Copyright (c) 2025 Reliant Labs

/**
 * React Query hooks for native dashboards (DashboardService, decision DB).
 *
 * The index is keyed by (org, project, environment) and shared: the
 * Dashboards page, the nav's starred entries and a dashboard's own star all
 * read the same cache entry, so starring on one surface shows on the others
 * without a refetch. Every write invalidates the whole dashboards subtree of
 * the org — a save changes the index row (version, editor), the document and
 * its history at once.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { useObservabilityApi, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { DashboardIndex, DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import { obsRetry } from "@/services/controlPlane/observability/errors";

/** A dashboard that is not there (NotFound) is an answer, not a blip: never retried. Otherwise the area's policy. */
export function dashboardRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ConnectError && error.code === Code.NotFound) return false;
  return obsRetry(failureCount, error);
}

export const dashboardKeys = {
  all: (orgId: string | null) => ["dashboards", orgId ?? ""] as const,
  index: (orgId: string | null, projectId: string, envId: string) => [...dashboardKeys.all(orgId), "index", projectId, envId] as const,
  templates: (orgId: string | null) => [...dashboardKeys.all(orgId), "templates"] as const,
  doc: (orgId: string | null, ref: DashboardRef | null, version: number, release: string) =>
    [...dashboardKeys.all(orgId), "doc", ref?.kind ?? "", ref?.projectId ?? "", ref?.key ?? "", version, release] as const,
  versions: (orgId: string | null, ref: DashboardRef | null) =>
    [...dashboardKeys.all(orgId), "versions", ref?.kind ?? "", ref?.projectId ?? "", ref?.key ?? ""] as const,
};

/** The merged index for one project, narrowed to one environment (by id) or none (every environment). */
export function useDashboardIndex(orgId: string | null, projectId: string, envId: string | undefined) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: dashboardKeys.index(orgId, projectId, envId ?? ""),
    queryFn: () => api.listDashboards({ orgId: orgId as string, projectId, envId }),
    enabled: !!orgId,
    staleTime: 60_000,
    retry: dashboardRetry,
  });
}

export function useDashboardTemplates(orgId: string | null, enabled = true) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: dashboardKeys.templates(orgId),
    queryFn: () => api.listDashboardTemplates(orgId as string),
    enabled: !!orgId && enabled,
    staleTime: 10 * 60_000,
    retry: dashboardRetry,
  });
}

/** One dashboard; `version` (workspace) or `release` (release) opens it as it was. */
export function useDashboard(orgId: string | null, ref: DashboardRef | null, version?: number, release?: string) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: dashboardKeys.doc(orgId, ref, version ?? 0, release ?? ""),
    queryFn: () => api.getDashboard({ orgId: orgId as string, ref: ref as DashboardRef, version, releaseVersion: release }),
    enabled: !!orgId && !!ref,
    staleTime: 30_000,
    retry: dashboardRetry,
  });
}

export function useDashboardVersions(orgId: string | null, ref: DashboardRef | null, enabled: boolean) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: dashboardKeys.versions(orgId, ref),
    queryFn: () => api.listDashboardVersions(orgId as string, ref as DashboardRef),
    enabled: !!orgId && !!ref && enabled,
    staleTime: 30_000,
    retry: dashboardRetry,
  });
}

/** Every dashboard write: it runs, then re-reads everything about the org's dashboards. */
export function useDashboardMutation<TArgs, TResult>(orgId: string | null, run: (api: ObservabilityApi, orgId: string, args: TArgs) => Promise<TResult>) {
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (args: TArgs) => run(api, orgId as string, args),
    onSettled: () => queryClient.invalidateQueries({ queryKey: dashboardKeys.all(orgId) }),
  });
}

/**
 * Star or unstar, shown at once: every cached index row for the dashboard
 * flips before the server answers, and flips back if it refuses.
 */
export function useStarDashboard(orgId: string | null) {
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  const sameRef = (a: DashboardRef, b: DashboardRef) => a.kind === b.kind && a.projectId === b.projectId && a.key === b.key;
  const flip = (ref: DashboardRef, starred: boolean) =>
    queryClient.setQueriesData<DashboardIndex>({ queryKey: [...dashboardKeys.all(orgId), "index"] }, (index) =>
      index ? { ...index, dashboards: index.dashboards.map((d) => (sameRef(d.ref, ref) ? { ...d, starred } : d)) } : index
    );
  return useMutation({
    mutationFn: ({ ref, starred }: { ref: DashboardRef; starred: boolean }) => api.starDashboard(orgId as string, ref, starred),
    onMutate: ({ ref, starred }) => flip(ref, starred),
    onError: (_error, { ref, starred }) => flip(ref, !starred),
    onSettled: () => queryClient.invalidateQueries({ queryKey: dashboardKeys.all(orgId) }),
  });
}
