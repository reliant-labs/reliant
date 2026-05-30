/**
 * Cloud-only wrappers around `controlplane.v1.BillingService`. Onboarding's
 * cloud-eligibility check is the only consumer today; once the cloud account
 * dashboard lands, more billing reads will hang off this module.
 */

import { BillingService } from "@/gen/controlplane/v1/public/billing_service_pb";
import type {
  ReliantEntitlement,
  ManagedReliantAccess,
} from "@/gen/controlplane/v1/public/shared_pb";
import { getControlPlaneClient } from "./client";

export type { ReliantEntitlement, ManagedReliantAccess };

/**
 * GetCurrentUserReliantState fetches the user's Reliant cloud entitlement +
 * managed-access record. Returned object mirrors the proto response — both
 * fields may be undefined for users without a cloud subscription.
 */
export async function getReliantState(): Promise<{
  entitlement?: ReliantEntitlement;
  managedAccess?: ManagedReliantAccess;
}> {
  const res = await getControlPlaneClient(
    BillingService,
  ).getCurrentUserReliantState({});
  return {
    entitlement: res.entitlement,
    managedAccess: res.managedAccess,
  };
}

/** True iff the entitlement makes this user cloud-eligible (active + flag on). */
export function isCloudEligible(
  entitlement: ReliantEntitlement | undefined,
): boolean {
  if (!entitlement) return false;
  return entitlement.status === "active" && entitlement.reliantEnabled === true;
}
