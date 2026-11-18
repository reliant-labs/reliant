import { queryOptions } from "@tanstack/react-query";

import { orgQueryEnabled, withOrgFailure, type OrgScope } from "@/lib/orgScope";
import { getComputeEligibility } from "@/services/controlPlane/billing";

/**
 * The one React Query key for GetCurrentUserComputeEligibility, in a module of
 * its own so the Add machine sheet, onboarding and Settings share the cache
 * entry (a purchase refetches it once, for every reader) without importing
 * each other's hooks.
 *
 * This is the family: every org's entry is under it, so an invalidation by it
 * refetches them all. One entry is `computeEligibilityQuery(org)`'s key.
 */
export const computeEligibilityQueryKey = ["onboarding", "computeEligibility"] as const;

/**
 * The eligibility read in `org`: funding is the org's (a subscription, granted
 * minutes), so the request names the org, the key does too, and nothing is
 * read before the org is known (lib/orgScope).
 */
export function computeEligibilityQuery(org: OrgScope, enabled = true) {
  return withOrgFailure(
    queryOptions({
      queryKey: [...computeEligibilityQueryKey, org.orgId ?? ""] as const,
      queryFn: () => getComputeEligibility(org.orgId),
      enabled: enabled && orgQueryEnabled(org),
      staleTime: 30_000,
    }),
    org,
  );
}
