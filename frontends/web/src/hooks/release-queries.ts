// Copyright (c) 2025 Reliant Labs

/**
 * The Releases pages' reads (services/forge/releases.ts), through the
 * ReleasesApi seam so tests and the preview can serve fixtures.
 *
 * Everything is keyed under `["forge", "releases", <org>, …]`, so a promote
 * invalidates the whole area in one call (useApplyForgePromote), and one
 * org's ledger is never served as another's: a forge project name and an
 * environment are read in the tab's org, which the key and the request both
 * name, and nothing is read before it is known (lib/orgScope). No poll: the
 * ledger moves when somebody promotes, and that path invalidates it — except
 * a rollout, which converges on its own and is re-read while it is moving.
 */

import { queryOptions, useQueries, useQuery } from "@tanstack/react-query";

import type { ReleasePromotion, ReleaseRollout } from "@/services/forge/releases";
import { useReleasesApi } from "@/services/forge/ReleasesApiContext";
import { orgQueryEnabled, useOrgScope, withOrgFailure } from "@/lib/orgScope";

import { forgeRetry } from "./forge-model";

type Org = string | null;

export const releaseKeys = {
  root: ["forge", "releases"] as const,
  /** Everything read in one org ("" for none). */
  org: (orgId: Org) => [...releaseKeys.root, orgId ?? ""] as const,
  list: (orgId: Org, project: string) => [...releaseKeys.org(orgId), "list", project] as const,
  detail: (orgId: Org, project: string, version: string) => [...releaseKeys.org(orgId), "detail", project, version] as const,
  compare: (orgId: Org, project: string, from: string, to: string, environmentId: string) =>
    [...releaseKeys.org(orgId), "compare", project, from, to, environmentId] as const,
  compareWorkload: (orgId: Org, project: string, from: string, to: string, environmentId: string, workload: string) =>
    [...releaseKeys.org(orgId), "compare", project, from, to, environmentId, "workload", workload] as const,
  promotions: (orgId: Org, environmentId: string) => [...releaseKeys.org(orgId), "promotions", environmentId] as const,
  rollout: (orgId: Org, environmentId: string, promotionId: string) =>
    [...releaseKeys.org(orgId), "rollout", environmentId, promotionId] as const,
  bundle: (orgId: Org, environmentId: string, bundleId: string, digest: string) =>
    [...releaseKeys.org(orgId), "bundle", environmentId, bundleId, digest] as const,
  deployRuns: (orgId: Org, project: string, env: string) => [...releaseKeys.org(orgId), "deploy-runs", project, env] as const,
  deployRun: (orgId: Org, id: string) => [...releaseKeys.org(orgId), "deploy-run", id] as const,
};

/**
 * The project's deploys, newest first (one environment's when `env` names
 * one). Re-read every 10s while one is running — a deploy moves on its own,
 * and the reader is usually watching it — and every minute otherwise, since a
 * deploy can start from any terminal without this page knowing.
 */
export function useDeployRuns(project: string | null | undefined, env?: string) {
  const org = useOrgScope();
  const api = useReleasesApi(org.orgId);
  return useQuery(
    withOrgFailure(
      queryOptions({
        queryKey: releaseKeys.deployRuns(org.orgId, project ?? "", env ?? ""),
        queryFn: () => api.listDeployRuns(project as string, env),
        enabled: !!project && orgQueryEnabled(org),
        staleTime: 10_000,
        retry: forgeRetry,
        refetchInterval: (query) => ((query.state.data ?? []).some((run) => run.state === "running") ? 10_000 : 60_000),
      }),
      org,
    ),
  );
}

/** One deploy with its log, re-read while it runs. */
export function useDeployRun(id: string | null) {
  const org = useOrgScope();
  const api = useReleasesApi(org.orgId);
  return useQuery(
    withOrgFailure(
      queryOptions({
        queryKey: releaseKeys.deployRun(org.orgId, id ?? ""),
        queryFn: () => api.getDeployRun(id as string),
        enabled: !!id && orgQueryEnabled(org),
        retry: forgeRetry,
        refetchInterval: (query) => (query.state.data?.run.state === "running" ? 5_000 : false),
      }),
      org,
    ),
  );
}

/** The project's releases, newest first. */
export function useProjectReleases(project: string | null | undefined) {
  const org = useOrgScope();
  const api = useReleasesApi(org.orgId);
  return useQuery(
    withOrgFailure(
      queryOptions({
        queryKey: releaseKeys.list(org.orgId, project ?? ""),
        queryFn: () => api.listReleases(project as string),
        enabled: !!project && orgQueryEnabled(org),
        staleTime: 15_000,
        retry: forgeRetry,
      }),
      org,
    ),
  );
}

/** One release; `data` is null when the project has no such release. */
export function useProjectRelease(project: string | null | undefined, version: string | null | undefined) {
  const org = useOrgScope();
  const api = useReleasesApi(org.orgId);
  return useQuery(
    withOrgFailure(
      queryOptions({
        queryKey: releaseKeys.detail(org.orgId, project ?? "", version ?? ""),
        queryFn: () => api.getRelease(project as string, version as string),
        enabled: !!project && !!version && orgQueryEnabled(org),
        staleTime: 15_000,
        retry: forgeRetry,
      }),
      org,
    ),
  );
}

/** What changed from `from` to `to`. Records only, so it is cached for as long as the page lives. */
export function useReleaseComparison(project: string, from: string | null, to: string, environmentId: string | null) {
  const org = useOrgScope();
  const api = useReleasesApi(org.orgId);
  return useQuery(
    withOrgFailure(
      queryOptions({
        queryKey: releaseKeys.compare(org.orgId, project, from ?? "", to, environmentId ?? ""),
        queryFn: () => api.compareReleases({ project, from: from as string, to, environmentId: environmentId ?? undefined }),
        enabled: !!project && !!from && !!to && from !== to && orgQueryEnabled(org),
        staleTime: 5 * 60_000,
        retry: forgeRetry,
      }),
      org,
    ),
  );
}

/** What changed from `from` to `to` in one environment, for one workload's objects (the workload History tab). */
export function useWorkloadReleaseComparison(project: string, from: string | null, to: string, environmentId: string, workload: string) {
  const org = useOrgScope();
  const api = useReleasesApi(org.orgId);
  return useQuery(
    withOrgFailure(
      queryOptions({
        queryKey: releaseKeys.compareWorkload(org.orgId, project, from ?? "", to, environmentId, workload),
        queryFn: () => api.compareReleases({ project, from: from as string, to, environmentId, workload }),
        enabled: !!project && !!from && !!to && from !== to && !!environmentId && !!workload && orgQueryEnabled(org),
        staleTime: 5 * 60_000,
        retry: forgeRetry,
      }),
      org,
    ),
  );
}

export interface PromotionsEntry {
  data?: ReleasePromotion[];
  isLoading: boolean;
  error: unknown;
}

/** Each environment's promotion ledger, newest first: one cache entry per environment, shared by both pages. */
export function useEnvPromotions(environmentIds: readonly string[]): Map<string, PromotionsEntry> {
  const org = useOrgScope();
  const api = useReleasesApi(org.orgId);
  const results = useQueries({
    queries: environmentIds.map((id) =>
      withOrgFailure(
        queryOptions({
          queryKey: releaseKeys.promotions(org.orgId, id),
          queryFn: () => api.listPromotions(id),
          enabled: orgQueryEnabled(org),
          staleTime: 15_000,
          retry: forgeRetry,
        }),
        org,
      ),
    ),
  });
  const byId = new Map<string, PromotionsEntry>();
  environmentIds.forEach((id, index) => {
    const result = results[index];
    byId.set(id, { data: result?.data, isLoading: !!result?.isLoading, error: result?.error ?? null });
  });
  return byId;
}

const SETTLED: readonly ReleaseRollout["phase"][] = ["succeeded", "superseded", "degraded"];

/** One promotion's rollout. Re-read every 10s while it is still moving. */
export function usePromotionRollout(environmentId: string | null, promotionId: string | null) {
  const org = useOrgScope();
  const api = useReleasesApi(org.orgId);
  return useQuery(
    withOrgFailure(
      queryOptions({
        queryKey: releaseKeys.rollout(org.orgId, environmentId ?? "", promotionId ?? ""),
        queryFn: () => api.getRollout(environmentId as string, promotionId as string),
        enabled: !!environmentId && !!promotionId && orgQueryEnabled(org),
        staleTime: 10_000,
        refetchInterval: (query) => {
          const phase = query.state.data?.phase;
          return query.state.error || !phase || SETTLED.includes(phase) ? false : 10_000;
        },
        retry: forgeRetry,
      }),
      org,
    ),
  );
}

/** The bundle a promotion applied. Immutable once recorded. */
export function usePromotionBundle(environmentId: string | null, bundleId: string, digest: string) {
  const org = useOrgScope();
  const api = useReleasesApi(org.orgId);
  return useQuery(
    withOrgFailure(
      queryOptions({
        queryKey: releaseKeys.bundle(org.orgId, environmentId ?? "", bundleId, digest),
        queryFn: () => api.getBundle(environmentId as string, bundleId, digest),
        enabled: !!environmentId && (!!bundleId || !!digest) && orgQueryEnabled(org),
        staleTime: Infinity,
        retry: forgeRetry,
      }),
      org,
    ),
  );
}
