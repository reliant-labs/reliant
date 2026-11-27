// Copyright (c) 2025 Reliant Labs

/**
 * The Releases pages' reads, on DeployService's generated hooks
 * (hooks/controlplane/deploy-service-hooks_gen.ts), each mapped through
 * services/forge/releases.ts and deployAttempts.ts in `select`.
 *
 * Every request names the tab's org (lib/orgScope), so its generated key does
 * too: one org's ledger is never served as another's, and nothing is read
 * before the org is known. A forge project name and an environment are read in
 * that org. A promote invalidates the whole area in the org it ran in, in one
 * call (invalidateReleaseReadsInOrg). No poll: the ledger moves when somebody
 * promotes, and that path invalidates it — except a rollout or a deploy, which
 * move on their own and are re-read while they are moving.
 *
 * Under a TransportScope (lib/transportScope.tsx: the forge tour's sample app,
 * a test's fixture ledger) the generated hooks read the scope's transport.
 */

import type { QueryClient, QueryFunction } from "@tanstack/react-query";
import { useQueries, useQuery } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { DeployAttemptState } from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { GetDeployReleaseResponseSchema, type GetDeployReleaseResponse } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { deployServiceKeys, deployServiceQueries } from "@/hooks/controlplane/deploy-service-hooks_gen";
import { orgQueryEnabled, orgRequest, requestOrgOfKey, useOrgScope, withOrgFailure } from "@/lib/orgScope";
import { DEPLOY_RUN_LIST_LIMIT, toDeployRunDetail, toDeployRuns } from "@/services/forge/deployAttempts";
import {
  PROMOTIONS_PAGE,
  RELEASE_LIST_LIMIT,
  toPromotionBundle,
  toPromotionRollout,
  toProjectRelease,
  toProjectReleases,
  toReleaseComparison,
  toReleasePromotions,
  type ReleasePromotion,
  type ReleaseRollout,
} from "@/services/forge/releases";

import { forgeRetry } from "./forge-model";

/** Every DeployService read the Releases pages make: what a promote refetches. */
const RELEASE_READS = [
  deployServiceKeys.listReleases.all,
  deployServiceKeys.getRelease.all,
  deployServiceKeys.compareReleases.all,
  deployServiceKeys.listPromotions.all,
  deployServiceKeys.getRollout.all,
  deployServiceKeys.getBundle.all,
  deployServiceKeys.listDeployAttempts.all,
  deployServiceKeys.getDeployAttempt.all,
] as const;

/** Refetch every Releases read made in `orgId` (null: those that named none), and no other org's. */
export function invalidateReleaseReadsInOrg(queryClient: QueryClient, orgId: string | null): Promise<void> {
  const inOrg = (query: { queryKey: readonly unknown[] }) => requestOrgOfKey(query.queryKey) === (orgId || null);
  return Promise.all(RELEASE_READS.map((queryKey) => queryClient.invalidateQueries({ queryKey, predicate: inOrg }))).then(() => undefined);
}

/**
 * The project's deploys, newest first (one environment's when `env` names
 * one). Re-read every 10s while one is running — a deploy moves on its own,
 * and the reader is usually watching it — and every minute otherwise, since a
 * deploy can start from any terminal without this page knowing.
 */
export function useDeployRuns(project: string | null | undefined, env?: string) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      deployServiceQueries.listDeployAttempts(orgRequest({ project: project ?? "", environmentName: env, limit: DEPLOY_RUN_LIST_LIMIT }, org.orgId), {
        enabled: !!project && orgQueryEnabled(org),
        staleTime: 0,
        retry: forgeRetry,
        refetchInterval: (query) => ((query.state.data?.attempts ?? []).some((attempt) => attempt.state === DeployAttemptState.RUNNING) ? 10_000 : 60_000),
        select: toDeployRuns,
      }),
      org,
    ),
  );
}

/** One deploy with its log, re-read while it runs. */
export function useDeployRun(id: string | null) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      deployServiceQueries.getDeployAttempt(orgRequest({ id: id ?? "" }, org.orgId), {
        enabled: !!id && orgQueryEnabled(org),
        retry: forgeRetry,
        refetchInterval: (query) => (query.state.data?.attempt?.state === DeployAttemptState.RUNNING ? 5_000 : false),
        select: (res) => toDeployRunDetail(res, id ?? ""),
      }),
      org,
    ),
  );
}

/** The project's releases, newest first. */
export function useProjectReleases(project: string | null | undefined) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      deployServiceQueries.listReleases(orgRequest({ project: project ?? "", limit: RELEASE_LIST_LIMIT }, org.orgId), {
        enabled: !!project && orgQueryEnabled(org),
        staleTime: 15_000,
        retry: forgeRetry,
        select: (res) => toProjectReleases(res, project ?? ""),
      }),
      org,
    ),
  );
}

/**
 * One release; `data` is null when the project has no such release.
 *
 * GetRelease answers a release it does not hold with NotFound. That is an
 * answer, not a failure, so it is cached as the empty response (no release),
 * which toProjectRelease reads as null — never retried, never an error state.
 */
export function useProjectRelease(project: string | null | undefined, version: string | null | undefined) {
  const org = useOrgScope();
  const options = deployServiceQueries.getRelease(orgRequest({ project: project ?? "", version: version ?? "" }, org.orgId), {
    enabled: !!project && !!version && orgQueryEnabled(org),
    staleTime: 15_000,
    retry: forgeRetry,
    select: toProjectRelease,
  });
  const fetchRelease = options.queryFn as QueryFunction<GetDeployReleaseResponse, readonly unknown[]>;
  return useQuery(
    withOrgFailure(
      {
        ...options,
        queryFn: async (context: Parameters<typeof fetchRelease>[0]) => {
          try {
            return await fetchRelease(context);
          } catch (err) {
            if (err instanceof ConnectError && err.code === Code.NotFound) return create(GetDeployReleaseResponseSchema);
            throw err;
          }
        },
      },
      org,
    ),
  );
}

/** What changed from `from` to `to`. Records only, so it is cached for as long as the page lives. */
export function useReleaseComparison(project: string, from: string | null, to: string, environmentId: string | null) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      deployServiceQueries.compareReleases(orgRequest({ project, fromVersion: from ?? "", toVersion: to, environmentId: environmentId ?? "" }, org.orgId), {
        enabled: !!project && !!from && !!to && from !== to && orgQueryEnabled(org),
        staleTime: 5 * 60_000,
        retry: forgeRetry,
        select: toReleaseComparison,
      }),
      org,
    ),
  );
}

/** What changed from `from` to `to` in one environment, for one workload's objects (the workload History tab). */
export function useWorkloadReleaseComparison(project: string, from: string | null, to: string, environmentId: string, workload: string) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      deployServiceQueries.compareReleases(
        orgRequest({ project, fromVersion: from ?? "", toVersion: to, environmentId, ...(workload ? { workload } : {}) }, org.orgId),
        {
          enabled: !!project && !!from && !!to && from !== to && !!environmentId && !!workload && orgQueryEnabled(org),
          staleTime: 5 * 60_000,
          retry: forgeRetry,
          select: toReleaseComparison,
        },
      ),
      org,
    ),
  );
}

export interface PromotionsEntry {
  data?: ReleasePromotion[];
  isLoading: boolean;
  error: unknown;
}

/** One environment's promotion ledger request in `orgId`: the key every reader of that ledger shares. */
export function promotionsRequest(environmentId: string, orgId: string | null) {
  return orgRequest({ environmentId, limit: PROMOTIONS_PAGE }, orgId);
}

/** Each environment's promotion ledger, newest first: one cache entry per environment, shared by both pages. */
export function useEnvPromotions(environmentIds: readonly string[]): Map<string, PromotionsEntry> {
  const org = useOrgScope();
  const results = useQueries({
    queries: environmentIds.map((id) =>
      withOrgFailure(
        deployServiceQueries.listPromotions(promotionsRequest(id, org.orgId), {
          enabled: orgQueryEnabled(org),
          staleTime: 15_000,
          retry: forgeRetry,
          select: toReleasePromotions,
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
  return useQuery(
    withOrgFailure(
      deployServiceQueries.getRollout(orgRequest({ environmentId: environmentId ?? "", promotionId: promotionId ?? "" }, org.orgId), {
        enabled: !!environmentId && !!promotionId && orgQueryEnabled(org),
        staleTime: 10_000,
        refetchInterval: (query) => {
          const phase = query.state.data ? toPromotionRollout(query.state.data)?.phase : undefined;
          return query.state.error || !phase || SETTLED.includes(phase) ? false : 10_000;
        },
        retry: forgeRetry,
        select: toPromotionRollout,
      }),
      org,
    ),
  );
}

/** The bundle a promotion applied. Immutable once recorded. */
export function usePromotionBundle(environmentId: string | null, bundleId: string, digest: string) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      deployServiceQueries.getBundle(orgRequest({ environmentId: environmentId ?? "", bundleId, digest }, org.orgId), {
        enabled: !!environmentId && (!!bundleId || !!digest) && orgQueryEnabled(org),
        staleTime: Infinity,
        retry: forgeRetry,
        select: toPromotionBundle,
      }),
      org,
    ),
  );
}
