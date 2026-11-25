/**
 * BillingService at the transport, for tests of code that reads it through
 * the generated hooks (hooks/controlplane/billing-service-hooks_gen.ts).
 *
 *   beforeEach(() => setTransport(mockTransport(billingHandlers({ listPlans: () => plans }))));
 *   afterEach(() => setTransport(null));
 *
 * `eligibilityHandlers` answers GetCurrentUserComputeEligibility from a fake
 * that returns the UI's ComputeEligibility (numbers), so a test keeps stating
 * its fixture in frontend terms.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";

import type { GetCurrentUserComputeEligibilityResponseSchema } from "@/gen/controlplane/services/billing/v1/billing_pb";
import type { MockHandlers } from "@/lib/test-utils";
import type { ComputeEligibility } from "@/services/controlPlane/billing";
import type { RedeemCouponResult } from "@/services/controlPlane/reliantAI";
import { serviceHandlers } from "./serviceTransport";

/** Handlers for BillingService from client-shaped fakes (`{ listPlans, getCurrentUserComputeSubscription, … }`). */
export function billingHandlers(fakes: Record<string, (request: never) => unknown>): MockHandlers {
  return serviceHandlers("BillingService", fakes);
}

/** The wire answer for a UI-shaped eligibility (int64 as bigint). */
export function eligibilityToWire(
  e: Partial<ComputeEligibility>,
): MessageInitShape<typeof GetCurrentUserComputeEligibilityResponseSchema> {
  return { ...e, grantedSmallDaemonSecondsRemaining: BigInt(e.grantedSmallDaemonSecondsRemaining ?? 0) };
}

/** GetCurrentUserComputeEligibility, answered by `fake` in UI terms. */
export function eligibilityHandlers(
  fake: (request: { orgId?: string }) => Partial<ComputeEligibility> | Promise<Partial<ComputeEligibility>>,
): MockHandlers {
  return billingHandlers({
    getCurrentUserComputeEligibility: async (request: { orgId?: string }) => eligibilityToWire(await fake(request)),
  });
}

/**
 * RedeemCoupon, answered by `fake(code)` in UI terms (the RedeemCouponResult
 * the hook hands its caller: numbers, not int64 bigints).
 */
export function redeemCouponHandlers(
  fake: (code: string) => Partial<RedeemCouponResult> | Promise<Partial<RedeemCouponResult>>,
): MockHandlers {
  return billingHandlers({
    redeemCoupon: async (request: { code: string }) => {
      const r = await fake(request.code);
      return {
        ...r,
        amountCents: BigInt(r.amountCents ?? 0),
        newBalanceCents: BigInt(r.newBalanceCents ?? 0),
        computeMinutes: BigInt(r.computeMinutes ?? 0),
        newGrantedSmallDaemonSecondsRemaining: BigInt(r.newGrantedSmallDaemonSecondsRemaining ?? 0),
      };
    },
  });
}
