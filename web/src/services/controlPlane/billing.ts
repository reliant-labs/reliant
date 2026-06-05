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

/**
 * GetCurrentUserBillingEmail returns the user-supplied billing_email override
 * and the JWT-sourced fallback. Used by the "Set your billing email" modal to
 * seed its input and to show which address Stripe currently sees.
 */
export async function getBillingEmail(): Promise<{
  billingEmail: string;
  fallbackEmail: string;
}> {
  const res = await getControlPlaneClient(
    BillingService,
  ).getCurrentUserBillingEmail({});
  return {
    billingEmail: res.billingEmail ?? "",
    fallbackEmail: res.fallbackEmail ?? "",
  };
}

/**
 * UpdateBillingEmail saves a new billing address (empty clears the override).
 * Errors propagate so the modal can show "valid billing email is required"
 * verbatim when the backend rejects an address.
 */
export async function updateBillingEmail(email: string): Promise<void> {
  await getControlPlaneClient(BillingService).updateBillingEmail({ email });
}
