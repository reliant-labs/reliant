// Copyright (c) 2026 Reliant Labs

/**
 * The organization billing reads behind the "Spend and usage" and "Billing
 * and infrastructure" dashboards: `ListOrganizationUsage` (history) and
 * `GetOrganizationCostSummary` (current periods and allowances).
 *
 * Neither retries: a PermissionDenied (no billing:read) is a settled answer
 * the view renders, and any other failure is shown as unavailable with a
 * Refresh, never papered over with an old or empty result.
 *
 * Every key names the organization, and every answer must name it back: an
 * answer for any other organization never enters the cache. The read asks
 * once more, and if the second answer is foreign too it fails like any other
 * read (forOrg).
 *
 * Each read goes over the nearest TransportScope's transport, else the app's
 * (lib/transportScope.tsx). The storage pool and project cost reads run on
 * the generated BillingService hooks, which resolve it from the QueryClient
 * they run on. The usage history and the cost summary stay hand-written,
 * because forOrg may send a second request inside one fetch.
 */

import { useQuery } from "@tanstack/react-query";

import { transport as appTransport } from "@/lib/connect";
import { logger } from "@/lib/logger";
import { useTransport } from "@/lib/transportScope";
import { getOrganizationCostSummary } from "@/services/controlPlane/observability/organizationCostsApi";
import { listOrganizationUsage, type OrganizationUsageRequest } from "@/services/controlPlane/observability/organizationUsageApi";
import { useGetProjectCost, useGetStorageUsage } from "./controlplane/billing-service-hooks_gen";

export const organizationUsageKeys = {
  all: (orgId: string) => ["organization-usage", orgId] as const,
  usage: (req: OrganizationUsageRequest) => ["organization-usage", req.orgId, req.startMs, req.endMs, req.granularity, req.groupBy] as const,
  /** The same key OrganizationCostsView reads, so the two views share one cached summary. */
  costSummary: (orgId: string) => ["organization-cost-summary", orgId] as const,
};

/** A billing answer that named another organization than the one asked about, twice. */
export class ForeignOrganizationAnswerError extends Error {
  readonly asked: string;
  readonly answered: string;

  constructor(asked: string, answered: string) {
    super(`the billing answer named organization "${answered}", not "${asked}"`);
    this.name = "ForeignOrganizationAnswerError";
    this.asked = asked;
    this.answered = answered;
  }
}

/**
 * `read`'s answer, which must name `orgId`. A foreign answer is logged (the
 * ids only) and asked for once more; a second foreign answer is an error, so
 * the panel says it could not load rather than show another organization's
 * figures.
 */
export async function forOrg<T extends { orgId: string }>(orgId: string, rpc: string, read: () => Promise<T>): Promise<T> {
  let answer = await read();
  if (answer.orgId === orgId) return answer;
  logger.warn(`${rpc}: the answer named another organization; asking again`, { asked: orgId, answered: answer.orgId || "(none)" });
  answer = await read();
  if (answer.orgId === orgId) return answer;
  logger.warn(`${rpc}: the answer named another organization again; not shown`, { asked: orgId, answered: answer.orgId || "(none)" });
  throw new ForeignOrganizationAnswerError(orgId, answer.orgId);
}

export function useOrganizationUsage(req: OrganizationUsageRequest | null) {
  const transport = useTransport(appTransport);
  return useQuery({
    queryKey: req ? organizationUsageKeys.usage(req) : ["organization-usage", "disabled"],
    queryFn: () => {
      const r = req as OrganizationUsageRequest;
      return forOrg(r.orgId, "ListOrganizationUsage", () => listOrganizationUsage(r, transport));
    },
    enabled: !!req?.orgId,
    staleTime: 60_000,
    retry: false,
  });
}

/** The organization's storage pool by product (BillingService.GetStorageUsage). */
export function useStorageUsage(orgId: string | null) {
  return useGetStorageUsage({ orgId: orgId ?? "" }, { enabled: !!orgId, staleTime: 60_000, retry: false });
}

export function useOrganizationCostSummary(orgId: string | null) {
  const transport = useTransport(appTransport);
  return useQuery({
    queryKey: organizationUsageKeys.costSummary(orgId ?? ""),
    queryFn: () => forOrg(orgId as string, "GetOrganizationCostSummary", () => getOrganizationCostSummary(orgId as string, transport)),
    enabled: !!orgId,
    staleTime: 60_000,
    retry: false,
  });
}

/** One project's hosting cost this month (BillingService.GetProjectCost): the project shell's Usage page. */
export function useProjectCost(orgId: string | null | undefined, projectId: string | null | undefined) {
  return useGetProjectCost(
    { orgId: orgId ?? "", projectId: projectId ?? "" },
    { enabled: !!orgId && !!projectId, staleTime: 60_000, retry: false },
  );
}
