// Copyright (c) 2025 Reliant Labs

/**
 * The reads both Releases pages share, joined into the release model: the
 * project's releases, each long-lived environment's promotion ledger, and the
 * names of the people who promoted (DeliveryInsights resolves a user id to a
 * display name; DeployService only records the id). One set of cache entries,
 * so moving from the list to a release and back costs no request.
 */

import { useMemo } from "react";

import { useObservabilityOrg, useReleases } from "@/hooks/observability-queries";
import { useEnvPromotions, useProjectReleases } from "@/hooks/release-queries";
import type { ReleasePromotion, ReleaseRecord } from "@/services/forge/releases";

import { buildReleaseModel, type PromotionsByEnv, type ReleaseEnv } from "./releaseModel";

const NAMES_WINDOW_MS = 366 * 86_400_000;

/**
 * user id → display name, from the promotions DeliveryInsights returns for
 * the project over the last year. Best effort: while it loads, or when the
 * caller may not read it, promoters show by their automation actor or not at
 * all — never as a raw id.
 */
export function usePromoterNames(project: string): ReadonlyMap<string, string> {
  const org = useObservabilityOrg();
  const scope = useMemo(() => (org.orgId ? { orgId: org.orgId, projectId: project, envIds: [] } : null), [org.orgId, project]);
  const ledger = useReleases(scope, "names:366d", () => ({ startMs: Date.now() - NAMES_WINDOW_MS, endMs: Date.now() }), { limit: 2000 });
  return useMemo(() => {
    const names = new Map<string, string>();
    for (const promotion of ledger.data?.promotions ?? []) {
      if (promotion.promotedByUserId && promotion.promotedBy) names.set(promotion.promotedByUserId, promotion.promotedBy);
    }
    return names;
  }, [ledger.data]);
}

/**
 * `extra` is a release the page is about that may be older than the listed
 * window (a link to a months-old release): it joins the model in cut order,
 * so its environment cells and timeline are judged like any other row.
 */
export function useReleaseData(project: string, envs: readonly ReleaseEnv[], extra?: ReleaseRecord | null) {
  const listed = useProjectReleases(project);
  const releases = useMemo(() => {
    if (!listed.data || !extra || listed.data.some((release) => release.version === extra.version)) return listed.data;
    return [...listed.data, extra].sort((a, b) => (b.createdAt ?? "").localeCompare(a.createdAt ?? ""));
  }, [listed.data, extra]);
  const longIds = useMemo(() => envs.filter((env) => env.kind === "long").map((env) => env.id), [envs]);
  const ledgers = useEnvPromotions(longIds);
  const names = usePromoterNames(project);

  // A stable key for the ledgers' contents, so the model is rebuilt only when one moved.
  const ledgerKey = longIds.map((id) => `${id}:${ledgers.get(id)?.data?.[0]?.id ?? ""}:${ledgers.get(id)?.data?.length ?? -1}`).join("|");
  const promotions = useMemo<PromotionsByEnv>(() => {
    const byId = new Map<string, ReleasePromotion[] | undefined>(longIds.map((id) => [id, ledgers.get(id)?.data]));
    return { get: (id) => byId.get(id) };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- ledgerKey stands for the ledgers' contents
  }, [ledgerKey]);

  const model = useMemo(
    () => (releases ? buildReleaseModel(releases, envs, promotions, names) : null),
    [releases, envs, promotions, names]
  );

  const ledgersLoading = longIds.some((id) => ledgers.get(id)?.isLoading);
  const ledgerFailed = longIds.filter((id) => !!ledgers.get(id)?.error).map((id) => envs.find((env) => env.id === id)?.name ?? id);

  return { releases: listed, ordered: releases ?? [], promotions, names, model, ledgersLoading, ledgerFailed };
}
