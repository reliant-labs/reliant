// Copyright (c) 2025 Reliant Labs

/**
 * The Releases pages' reads (services/forge/releases.ts), through the
 * ReleasesApi seam so tests and the preview can serve fixtures.
 *
 * Everything is keyed under `["forge", "releases", …]`, so a promote
 * invalidates the whole area in one call (useApplyForgePromote). No poll: the
 * ledger moves when somebody promotes, and that path invalidates it — except
 * a rollout, which converges on its own and is re-read while it is moving.
 */

import { useQueries, useQuery } from "@tanstack/react-query";

import type { ReleasePromotion, ReleaseRollout } from "@/services/forge/releases";
import { useReleasesApi } from "@/services/forge/ReleasesApiContext";

import { forgeRetry } from "./forge-model";

export const releaseKeys = {
  root: ["forge", "releases"] as const,
  list: (project: string) => [...releaseKeys.root, "list", project] as const,
  detail: (project: string, version: string) => [...releaseKeys.root, "detail", project, version] as const,
  compare: (project: string, from: string, to: string, environmentId: string) =>
    [...releaseKeys.root, "compare", project, from, to, environmentId] as const,
  promotions: (environmentId: string) => [...releaseKeys.root, "promotions", environmentId] as const,
  rollout: (environmentId: string, promotionId: string) => [...releaseKeys.root, "rollout", environmentId, promotionId] as const,
  bundle: (environmentId: string, bundleId: string, digest: string) =>
    [...releaseKeys.root, "bundle", environmentId, bundleId, digest] as const,
  deployRuns: (project: string, env: string) => [...releaseKeys.root, "deploy-runs", project, env] as const,
  deployRun: (id: string) => [...releaseKeys.root, "deploy-run", id] as const,
};

/**
 * The project's deploys, newest first (one environment's when `env` names
 * one). Re-read every 10s while one is running — a deploy moves on its own,
 * and the reader is usually watching it — and every minute otherwise, since a
 * deploy can start from any terminal without this page knowing.
 */
export function useDeployRuns(project: string | null | undefined, env?: string) {
  const api = useReleasesApi();
  return useQuery({
    queryKey: releaseKeys.deployRuns(project ?? "", env ?? ""),
    queryFn: () => api.listDeployRuns(project as string, env),
    enabled: !!project,
    staleTime: 10_000,
    retry: forgeRetry,
    refetchInterval: (query) => ((query.state.data ?? []).some((run) => run.state === "running") ? 10_000 : 60_000),
  });
}

/** One deploy with its log, re-read while it runs. */
export function useDeployRun(id: string | null) {
  const api = useReleasesApi();
  return useQuery({
    queryKey: releaseKeys.deployRun(id ?? ""),
    queryFn: () => api.getDeployRun(id as string),
    enabled: !!id,
    retry: forgeRetry,
    refetchInterval: (query) => (query.state.data?.run.state === "running" ? 5_000 : false),
  });
}

/** The project's releases, newest first. */
export function useProjectReleases(project: string | null | undefined) {
  const api = useReleasesApi();
  return useQuery({
    queryKey: releaseKeys.list(project ?? ""),
    queryFn: () => api.listReleases(project as string),
    enabled: !!project,
    staleTime: 15_000,
    retry: forgeRetry,
  });
}

/** One release; `data` is null when the project has no such release. */
export function useProjectRelease(project: string | null | undefined, version: string | null | undefined) {
  const api = useReleasesApi();
  return useQuery({
    queryKey: releaseKeys.detail(project ?? "", version ?? ""),
    queryFn: () => api.getRelease(project as string, version as string),
    enabled: !!project && !!version,
    staleTime: 15_000,
    retry: forgeRetry,
  });
}

/** What changed from `from` to `to`. Records only, so it is cached for as long as the page lives. */
export function useReleaseComparison(project: string, from: string | null, to: string, environmentId: string | null) {
  const api = useReleasesApi();
  return useQuery({
    queryKey: releaseKeys.compare(project, from ?? "", to, environmentId ?? ""),
    queryFn: () => api.compareReleases({ project, from: from as string, to, environmentId: environmentId ?? undefined }),
    enabled: !!project && !!from && !!to && from !== to,
    staleTime: 5 * 60_000,
    retry: forgeRetry,
  });
}

export interface PromotionsEntry {
  data?: ReleasePromotion[];
  isLoading: boolean;
  error: unknown;
}

/** Each environment's promotion ledger, newest first: one cache entry per environment, shared by both pages. */
export function useEnvPromotions(environmentIds: readonly string[]): Map<string, PromotionsEntry> {
  const api = useReleasesApi();
  const results = useQueries({
    queries: environmentIds.map((id) => ({
      queryKey: releaseKeys.promotions(id),
      queryFn: () => api.listPromotions(id),
      staleTime: 15_000,
      retry: forgeRetry,
    })),
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
  const api = useReleasesApi();
  return useQuery({
    queryKey: releaseKeys.rollout(environmentId ?? "", promotionId ?? ""),
    queryFn: () => api.getRollout(environmentId as string, promotionId as string),
    enabled: !!environmentId && !!promotionId,
    staleTime: 10_000,
    refetchInterval: (query) => {
      const phase = query.state.data?.phase;
      return query.state.error || !phase || SETTLED.includes(phase) ? false : 10_000;
    },
    retry: forgeRetry,
  });
}

/** The bundle a promotion applied. Immutable once recorded. */
export function usePromotionBundle(environmentId: string | null, bundleId: string, digest: string) {
  const api = useReleasesApi();
  return useQuery({
    queryKey: releaseKeys.bundle(environmentId ?? "", bundleId, digest),
    queryFn: () => api.getBundle(environmentId as string, bundleId, digest),
    enabled: !!environmentId && (!!bundleId || !!digest),
    staleTime: Infinity,
    retry: forgeRetry,
  });
}
