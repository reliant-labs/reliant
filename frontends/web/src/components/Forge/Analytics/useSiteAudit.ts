// Copyright (c) 2026 Reliant Labs

/**
 * The reads behind Site insights' SEO and Accessibility views, and behind a
 * site's audit scores anywhere else (the static site's page):
 *
 *   useAuditTarget()       which site and environment the view reads: the
 *                          URL's `?site=` and the console's environment scope,
 *                          over the project's hosted static sites (GetStatus)
 *   useSiteAudit(target)   GetSiteAudit's newest finished audit of it, polled
 *                          while one is queued or running
 *   useRunSiteAudit()      RunSiteAudit, which refreshes the read
 *   useSiteAuditScores()   one site's SEO and accessibility scores and their
 *                          deltas, for a summary card
 *
 * All reads go through the generated SiteAuditService hooks, so the demo's
 * and the sample tour's transport scope answers them like any other.
 */

import { useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { siteAuditServiceKeys, useGetSiteAudit, useRunSiteAudit as useRunSiteAuditRpc } from "@/hooks/controlplane/site-audit-service-hooks_gen";
import { useCloudEnvStatuses, useLiveView } from "@/hooks/forge-queries";
import { useObservabilityOrg } from "@/hooks/observability-queries";

import { useForgeConsole } from "../Console/consoleContext";
import { liveStateEnvs } from "../LiveState/liveStateModel";
import { useSiteChoice } from "./siteInsightsParts";
import { auditSitesOf, auditTargetOf, categoryScore, scoreDelta, siteAuditOf, type AuditCategory, type AuditSite, type AuditTarget, type SiteAudit } from "./siteAuditModel";

/** How often a view re-reads while an audit is queued or running. */
export const SITE_AUDIT_POLL_MS = 30_000;

/** The project's hosted static sites, each with the environments it is deployed in (prod first). */
export function useAuditSites(project: string): { sites: AuditSite[]; loading: boolean } {
  const live = useLiveView(project);
  const envs = useMemo(() => liveStateEnvs(live.data?.envs ?? []), [live.data]);
  const statuses = useCloudEnvStatuses(envs.map((env) => env.id));
  const loading = live.isLoading || envs.some((env) => statuses.get(env.id)?.isLoading);
  const sites = auditSitesOf(envs, new Map(envs.map((env) => [env.id, statuses.get(env.id)?.data?.workloads ?? []])));
  return { sites, loading };
}

/** Which site and environment the audit view reads, and the choices it has. */
export function useAuditTarget(): { target: AuditTarget; sites: AuditSite[]; loading: boolean; site: string | undefined; setSite: (site: string | undefined) => void } {
  const { forgeProject, env } = useForgeConsole();
  const { site, setSite } = useSiteChoice();
  const { sites, loading } = useAuditSites(forgeProject);
  const target = useMemo(() => auditTargetOf({ project: forgeProject, site, env, sites }), [forgeProject, site, env, sites]);
  return { target, sites, loading, site, setSite };
}

/** One site's newest finished audit (and the one in progress, and the quota). */
export function useSiteAudit(target: { envId: string; frontend: string } | null, { enabled = true }: { enabled?: boolean } = {}) {
  const org = useObservabilityOrg();
  const orgId = org.orgId ?? undefined;
  const req = target ? { selector: { case: "latest" as const, value: { environmentId: target.envId, frontend: target.frontend } }, orgId } : { selector: { case: undefined } };
  return useGetSiteAudit(req, {
    enabled: enabled && !!target && !!orgId,
    select: siteAuditOf,
    refetchInterval: (query) => (query.state.data?.inProgress ? SITE_AUDIT_POLL_MS : false),
  });
}

/** RunSiteAudit; on success every cached audit read refreshes, so the view shows the queued one. */
export function useRunSiteAudit() {
  const queryClient = useQueryClient();
  return useRunSiteAuditRpc({
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: siteAuditServiceKeys.getSiteAudit.all }),
  });
}

export interface SiteAuditScore {
  /** 0–100; null before the first audit, or when the pass that scores it has not run. */
  score: number | null;
  /** Against the previous release's audit; null with none. */
  delta: number | null;
  /** The release the score is of. */
  release: string;
}

/**
 * One site's SEO and accessibility scores, for a page that shows them beside
 * other things (the static site's page, Services/SiteDetail):
 *
 *   const scores = useSiteAuditScores({ envId, frontend });
 *   scores.seo.score // 0–100, or null before the first audit
 */
export function useSiteAuditScores(target: { envId: string; frontend: string } | null): Record<AuditCategory, SiteAuditScore> & { audit: SiteAudit | undefined; isLoading: boolean; error: unknown } {
  const query = useSiteAudit(target);
  const audit = query.data;
  const of = (category: AuditCategory): SiteAuditScore => {
    const score = categoryScore(audit?.audit ?? null, category);
    return { score, delta: scoreDelta(score, categoryScore(audit?.previous ?? null, category)), release: audit?.audit?.release ?? "" };
  };
  return { seo: of("seo"), accessibility: of("accessibility"), audit, isLoading: query.isLoading, error: query.error };
}
