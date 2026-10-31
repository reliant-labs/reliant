// Copyright (c) 2026 Reliant Labs

/**
 * The organization billing reads behind the "Spend and usage" and "Billing
 * and infrastructure" dashboards: `ListOrganizationUsage` (history) and
 * `GetOrganizationCostSummary` (current periods and allowances).
 *
 * Neither retries: a PermissionDenied (no billing:read) is a settled answer
 * the view renders, and any other failure is shown as unavailable with a
 * Refresh, never papered over with an old or empty result.
 */

import { useQuery } from "@tanstack/react-query";

import { getOrganizationCostSummary } from "@/services/controlPlane/observability/organizationCostsApi";
import { listOrganizationUsage, type OrganizationUsageRequest } from "@/services/controlPlane/observability/organizationUsageApi";

export const organizationUsageKeys = {
  all: (orgId: string) => ["organization-usage", orgId] as const,
  usage: (req: OrganizationUsageRequest) => ["organization-usage", req.orgId, req.startMs, req.endMs, req.granularity, req.groupBy] as const,
  /** The same key OrganizationCostsView reads, so the two views share one cached summary. */
  costSummary: (orgId: string) => ["organization-cost-summary", orgId] as const,
};

export function useOrganizationUsage(req: OrganizationUsageRequest | null) {
  return useQuery({
    queryKey: req ? organizationUsageKeys.usage(req) : ["organization-usage", "disabled"],
    queryFn: () => listOrganizationUsage(req as OrganizationUsageRequest),
    enabled: !!req?.orgId,
    staleTime: 60_000,
    retry: false,
  });
}

export function useOrganizationCostSummary(orgId: string | null) {
  return useQuery({
    queryKey: organizationUsageKeys.costSummary(orgId ?? ""),
    queryFn: () => getOrganizationCostSummary(orgId as string),
    enabled: !!orgId,
    staleTime: 60_000,
    retry: false,
  });
}
