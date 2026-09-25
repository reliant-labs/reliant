/**
 * Cloud-only wrappers around `controlplane.v1.BillingService`. Onboarding's
 * compute-eligibility check is the only consumer today; once the cloud
 * account dashboard lands, more billing reads will hang off this module.
 */

import {
  BillingService,
  ComputeIneligibleReason,
} from "@/gen/controlplane/services/billing/v1/billing_pb";
import type {
  ReliantEntitlement,
  ManagedReliantAccess,
} from "@/gen/controlplane/controlplane/v1/shared_pb";
import { getControlPlaneClient } from "./client";

export type { ReliantEntitlement, ManagedReliantAccess };
export { ComputeIneligibleReason };

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

export interface ComputeEligibility {
  eligible: boolean;
  reason: ComputeIneligibleReason;
  hasActiveSubscription: boolean;
  grantedMinutesRemaining: number;
  planName: string;
  /**
   * The daemon sizes the server will actually let this caller start, resolved
   * from their compute plan and falling back to plan_compute_free (["small"])
   * when there is none. Wire strings, matching `PlanLimits.allowed_daemon_sizes`.
   *
   * Read this instead of deriving sizes from the subscription: a coupon-funded
   * caller is eligible with NO subscription, so a client that asked the
   * subscription decided such a user could run nothing at all.
   */
  allowedDaemonSizes: string[];
}

/**
 * GetCurrentUserComputeEligibility answers whether the caller can start a
 * managed daemon right now, and why not — the authoritative gate lives in
 * the daemon service itself, this mirrors the same funding facts (active
 * compute subscription, including the signup trial, OR unspent granted
 * compute minutes from a redeemed coupon) so the UI can predict it instead
 * of gating on the unrelated LLM wallet entitlement.
 */
export async function getComputeEligibility(): Promise<ComputeEligibility> {
  const res = await getControlPlaneClient(
    BillingService,
  ).getCurrentUserComputeEligibility({});
  return {
    eligible: res.eligible,
    reason: res.reason,
    hasActiveSubscription: res.hasActiveSubscription,
    // Wire value is bigint (int64); the UI works in numbers, matching the
    // neighbouring compute-usage wrapper's convention.
    grantedMinutesRemaining: Number(res.grantedMinutesRemaining),
    planName: res.planName,
    allowedDaemonSizes: res.allowedDaemonSizes,
  };
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
