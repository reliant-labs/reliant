// Copyright (c) 2026 Reliant Labs

/**
 * An organization's org-funded AI usage (OrgUsageService.GetOrgLLMUsage),
 * for its billing:read holders. Every request names its org, so the cache
 * never mixes two orgs' answers. A refusal (no billing:read, not a member) is
 * not retried: it is an answer, not an outage.
 */

import type { OrgLLMUsageGroupBy } from "../gen/reliant/v1/org_usage_pb";
import { hasControlPlane } from "../services/controlPlane/config";
import { useAuthStore } from "../store/authStore";
import { useGetOrgLLMUsage } from "./org-usage-service-hooks_gen";

export interface OrgLLMUsageQuery {
  orgId: string | undefined;
  groupBy: OrgLLMUsageGroupBy;
  startDate: string;
  endDate: string;
}

export function useOrgLLMUsage({ orgId, groupBy, startDate, endDate }: OrgLLMUsageQuery) {
  const signedIn = useAuthStore((s) => s.user !== null);
  return useGetOrgLLMUsage(
    { orgId: orgId ?? "", groupBy, startDate, endDate },
    { enabled: hasControlPlane && signedIn && !!orgId, staleTime: 60_000, retry: false },
  );
}
