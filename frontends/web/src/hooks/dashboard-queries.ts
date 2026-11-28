// Copyright (c) 2025 Reliant Labs

/**
 * React Query hooks for native dashboards (DashboardService), on its
 * generated hooks (hooks/controlplane/dashboard-service-hooks_gen.ts): the
 * request is built and the answer mapped into the console's model in
 * services/controlPlane/observability/dashboardsApi.ts, and every call runs on
 * its QueryClient's transport (a TransportScope's: the forge tour's sample app,
 * a preview; else the app's).
 *
 * The index is keyed by its request (org, project, environment) and shared:
 * the Dashboards page, the nav's starred entries and a dashboard's own star
 * all read the same cache entry, so starring on one surface shows on the
 * others without a refetch. Every write re-reads the whole of the org's
 * dashboards, whether it succeeded or not — a save changes the index row
 * (version, editor), the document and its history at once, and a refused one
 * (someone saved in between) shows the version that won. Each write's call
 * site passes `useDashboardWriteOptions(orgId)` to its generated mutation for that.
 */

import { useQuery, useMutation, useQueryClient, type QueryClient, type UseQueryResult } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  dashboardServiceKeys,
  dashboardServiceMutations,
  useGetDashboard,
  useListDashboards,
  useListDashboardTemplates,
  useListDashboardVersions,
} from "@/hooks/controlplane/dashboard-service-hooks_gen";
import { keyNamesOrg } from "@/lib/orgScope";
import type { ListDashboardsResponse } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";
import type { DashboardDocument, DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import {
  dashboardDocumentOf,
  dashboardIndexOf,
  dashboardRefRequest,
  dashboardTemplatesOf,
  dashboardVersionsOf,
  getDashboardRequest,
  listDashboardsRequest,
  listDashboardVersionsRequest,
  refOfSummary,
  sameDashboardRef,
} from "@/services/controlPlane/observability/dashboardsApi";

import { obsRetry } from "@/services/controlPlane/observability/errors";
import { clientBuiltinDocument, isClientBuiltin, withClientBuiltins } from "@/services/controlPlane/observability/clientBuiltins";
import { useObsReadOnly } from "@/components/Forge/Observability/obsReadOnly";

/**
 * Whether the console reads the person's own account, so the client's own
 * built-ins (clientBuiltins.ts: their LLM usage) belong in it. A read-only
 * console is sample data (the public demo, the forge tour's sample app:
 * obsReadOnly.tsx), never theirs.
 */
function useOwnData(): boolean {
  return !useObsReadOnly().readOnly;
}

/** A dashboard that is not there (NotFound) is an answer, not a blip: never retried. Otherwise the area's policy. */
export function dashboardRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ConnectError && error.code === Code.NotFound) return false;
  return obsRetry(failureCount, error);
}

/**
 * Re-reads the org's dashboards: every DashboardService read whose request
 * names the org, or only its indexes (`indexOnly`). Another org's cached
 * dashboards are left alone.
 */
export function invalidateOrgDashboards(client: QueryClient, orgId: string | null, indexOnly = false): Promise<void> {
  return client.invalidateQueries({
    queryKey: indexOnly ? dashboardServiceKeys.listDashboards.all : dashboardServiceKeys.all,
    predicate: (query) => keyNamesOrg(query.queryKey, orgId ?? ""),
  });
}

/**
 * What every dashboard write's generated mutation takes: no refresh of the
 * generated scope, and instead the org's dashboards re-read once the write
 * settles, failed or not (the write stays pending until they are).
 */
export function useDashboardWriteOptions(orgId: string | null) {
  const queryClient = useQueryClient();
  return { invalidate: false as const, onSettled: () => invalidateOrgDashboards(queryClient, orgId) };
}

/**
 * The merged index for one project, narrowed to one environment (by id) or
 * none (every environment). `projectId` undefined is the whole org: the wire
 * field is omitted, which the server reads as every project ("" would mean
 * only the project-less dashboards).
 */
export function useDashboardIndex(orgId: string | null, projectId: string | undefined, envId: string | undefined) {
  const ownData = useOwnData();
  return useListDashboards(listDashboardsRequest({ orgId: orgId ?? "", projectId, envId }), {
    // The client's own built-ins are the person's (their LLM usage): never listed on sample data.
    select: (res) => {
      const index = dashboardIndexOf(res);
      return ownData ? withClientBuiltins(index, { projectId, envId }) : index;
    },
    enabled: !!orgId,
    staleTime: 60_000,
    retry: dashboardRetry,
  });
}

export function useDashboardTemplates(orgId: string | null, enabled = true) {
  return useListDashboardTemplates(
    { orgId: orgId ?? "" },
    {
      select: dashboardTemplatesOf,
      enabled: !!orgId && enabled,
      staleTime: 10 * 60_000,
      retry: dashboardRetry,
    }
  );
}

/** The client-owned built-in's document: no DashboardService document, so never a request. */
const CLIENT_BUILTIN_DOC_KEY = ["client-builtin-dashboard"] as const;

/** One dashboard; `version` (workspace) or `release` (release) opens it as it was. */
export function useDashboard(orgId: string | null, ref: DashboardRef | null, version?: number, release?: string): UseQueryResult<DashboardDocument, Error> {
  const ownData = useOwnData();
  // A client-owned built-in has no DashboardService document to fetch. On sample data it is not one: the
  // console's own API answers for it (not found), so a link to it never opens the person's usage there.
  const local = isClientBuiltin(ref) && ownData;
  const remote = useGetDashboard(getDashboardRequest({ orgId: orgId ?? "", ref, version, releaseVersion: release }), {
    select: dashboardDocumentOf,
    enabled: !!orgId && !!ref && !local,
    staleTime: 30_000,
    retry: dashboardRetry,
  });
  const builtin = useQuery({
    queryKey: CLIENT_BUILTIN_DOC_KEY,
    queryFn: () => clientBuiltinDocument(),
    enabled: !!orgId && local,
    staleTime: 30_000,
  });
  return local ? builtin : remote;
}

export function useDashboardVersions(orgId: string | null, ref: DashboardRef | null, enabled: boolean) {
  return useListDashboardVersions(listDashboardVersionsRequest(orgId ?? "", ref), {
    select: dashboardVersionsOf,
    enabled: !!orgId && !!ref && enabled,
    staleTime: 30_000,
    retry: dashboardRetry,
  });
}

/**
 * Star or unstar, shown at once: every cached index row for the dashboard
 * flips before the server answers, and flips back if it refuses. Two rpcs
 * (StarDashboard, UnstarDashboard) behind one toggle, so it runs the
 * generated writes of its QueryClient (its scope's transport) itself.
 */
export function useToggleDashboardStar(orgId: string | null) {
  const queryClient = useQueryClient();
  // The cache holds the wire answer; each reader maps it in `select`.
  const flip = (ref: DashboardRef, starred: boolean) =>
    queryClient.setQueriesData<ListDashboardsResponse>(
      { queryKey: dashboardServiceKeys.listDashboards.all, predicate: (query) => keyNamesOrg(query.queryKey, orgId ?? "") },
      (index) => (index ? { ...index, dashboards: index.dashboards.map((d) => (sameDashboardRef(refOfSummary(d), ref) ? { ...d, starred } : d)) } : index)
    );
  return useMutation({
    mutationFn: ({ ref, starred }: { ref: DashboardRef; starred: boolean }) => {
      const writes = dashboardServiceMutations(queryClient);
      const req = dashboardRefRequest(orgId as string, ref);
      return (starred ? writes.starDashboard(req, { invalidate: false }) : writes.unstarDashboard(req, { invalidate: false })).then(() => undefined);
    },
    onMutate: ({ ref, starred }) => flip(ref, starred),
    onError: (_error, { ref, starred }) => flip(ref, !starred),
    onSettled: () => invalidateOrgDashboards(queryClient, orgId),
  });
}

/**
 * Records that the reader opened a dashboard (Recently viewed), then re-reads
 * the org's indexes. Quietly: through the generated imperative write, never
 * React Query's mutation cache, so a view that could not be recorded shows
 * nothing.
 */
export function recordDashboardView(queryClient: QueryClient, orgId: string, ref: DashboardRef): Promise<void> {
  return dashboardServiceMutations(queryClient)
    .recordDashboardView(dashboardRefRequest(orgId, ref), { invalidate: (client) => void invalidateOrgDashboards(client, orgId, true) })
    .then(() => undefined);
}
