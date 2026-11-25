/**
 * A redeemed coupon must refresh the fact that GATES compute.
 *
 * ── The gap ───────────────────────────────────────────────────────────
 *
 * `useRedeemCoupon` invalidated four keys — the two wallet reads, the Reliant
 * overview, and the compute SUBSCRIPTION — but never
 * `computeEligibilityQueryKey` (GetCurrentUserComputeEligibility's generated
 * key scope).
 *
 * That is the one query `useOnboardingFacts` reads, and it is what
 * `requiresPayment` turns into "does this user still owe for a machine". A
 * compute coupon grants MINUTES, not a subscription, so invalidating the
 * subscription key refreshes a read that did not change while leaving the read
 * that did change stale for its full 30s `staleTime`.
 *
 * The consequence was invisible where redemption happened to refetch
 * eligibility by hand (onboarding's compute step does), and load-bearing
 * everywhere it did not — which is exactly the billing page, the place the
 * owner asked coupons to be moved TO. A code redeemed there granted minutes
 * server-side while every eligibility-driven surface in the app went on
 * believing the user had none.
 *
 * The fix belongs in the mutation, not at each call site: a caller that has to
 * remember which caches a coupon invalidates is a caller that will forget.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Hoisted: `vi.mock` factories run before module-level initialization, so a
// plain `const` here is in the temporal dead zone when the factory reads it.
const { mockRedeemCoupon } = vi.hoisted(() => ({
  mockRedeemCoupon: vi.fn(async () => ({
    kind: 1,
    computeMinutes: 6000,
    newGrantedSmallDaemonSecondsRemaining: 360000,
    amountCents: 0,
  })),
}));

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { redeemCouponHandlers } from "@/test/billingTransport";
import { useRedeemCoupon } from "../useReliantAIQueries";
import { computeEligibilityQueryKey } from "../useOnboardingQueries";

function wrapperFor(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  // useRedeemCoupon runs on the generated BillingService mutation.
  setTransport(mockTransport(redeemCouponHandlers(() => mockRedeemCoupon())));
});
afterEach(() => setTransport(null));

describe("useRedeemCoupon — cache invalidation", () => {
  it("invalidates compute eligibility, the fact that gates starting a machine", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const invalidated: unknown[] = [];
    const realInvalidate = client.invalidateQueries.bind(client);
    vi.spyOn(client, "invalidateQueries").mockImplementation((filters) => {
      invalidated.push(filters?.queryKey);
      return realInvalidate(filters);
    });

    const { result } = renderHook(() => useRedeemCoupon(), {
      wrapper: wrapperFor(client),
    });

    result.current.mutate("DEVTESTCOMPUTE");

    await waitFor(() => expect(mockRedeemCoupon).toHaveBeenCalled());
    await waitFor(() => {
      expect(invalidated).toContainEqual(computeEligibilityQueryKey);
    });
  });
});
