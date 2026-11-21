// Copyright (c) 2026 Reliant Labs

/**
 * The plan badge beside the org's name in the sidebar's top row (Vercel's
 * "Hobby"): the billing plan the app already reads (useComputeSubscription,
 * GetCurrentUserComputeSubscription, the plan Settings › Billing shows), as a
 * short word. No new RPC.
 *
 * Hidden — null — whenever the plan is not known to be this org's live plan:
 * still loading, the read failed, no subscription (an org without a plan pays
 * from its wallet; there is no "Free" tier to name), a canceled or unspecified
 * subscription, or one that belongs to another org.
 */

import { PlanTier, SubscriptionStatus, type Subscription } from "@/gen/controlplane/controlplane/v1/shared_pb";
import { useComputeSubscription } from "@/hooks/useCloudBillingQueries";

const TIER_LABELS: Partial<Record<PlanTier, string>> = {
  [PlanTier.STARTER]: "Starter",
  [PlanTier.PRO]: "Pro",
  [PlanTier.ENTERPRISE]: "Enterprise",
};

/** Statuses whose plan is the org's plan right now (a trial and a late payment included). */
const LIVE = new Set([SubscriptionStatus.ACTIVE, SubscriptionStatus.TRIALING, SubscriptionStatus.PAST_DUE]);

/**
 * The badge's word for `subscription` in `activeOrgId`, or null to hide it.
 * The tier when the plan has one (Starter, Pro, Enterprise); else the plan's
 * own name (a compute plan: "Compute M").
 */
export function orgPlanLabel(subscription: Pick<Subscription, "orgId" | "status" | "plan"> | null | undefined, activeOrgId: string): string | null {
  if (!subscription?.plan || !LIVE.has(subscription.status)) return null;
  if (subscription.orgId && subscription.orgId !== activeOrgId) return null;
  return TIER_LABELS[subscription.plan.tier] ?? (subscription.plan.name.trim() || null);
}

/** The active org's plan badge, or null while unknown. */
export function useOrgPlanLabel(activeOrgId: string): string | null {
  const subscription = useComputeSubscription();
  if (subscription.isPending || subscription.isError) return null;
  return orgPlanLabel(subscription.data?.subscription, activeOrgId);
}
