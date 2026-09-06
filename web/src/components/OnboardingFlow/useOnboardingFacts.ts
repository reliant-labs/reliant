/**
 * The two server facts onboarding's payment decision reads.
 *
 * Both come from queries that are already mounted and cached by the time they
 * matter — `ComputeStep` mounts `useCloudEligibility`, `ModelStep` mounts
 * `useWalletOverview` — so reading them again here costs no request.
 *
 * ── Pessimistic while loading, deliberately ───────────────────────────
 *
 * An in-flight query reports `computeEligible: false, walletFunded: false`,
 * i.e. "you owe money", not "you owe nothing". The two mistakes are not
 * symmetric: erring toward the checkout step shows a spinner for a moment and
 * then advances, while erring away from it walks an unpaid user past the only
 * place onboarding asks for payment. That is the dead end this whole change
 * exists to remove, reintroduced through a race.
 *
 * ── Why `refetch` returns the facts ───────────────────────────────────
 *
 * The checkout step must decide what to do the moment a purchase is confirmed,
 * and it must decide on FRESH server state. Returning the values (rather than
 * relying on a re-render) is what lets the commit fire from the confirmation
 * handler rather than from an effect watching state settle.
 */
import { useCallback } from "react";

import { useCloudEligibility } from "@/hooks/useOnboardingQueries";
import { useWalletOverview } from "@/hooks/useReliantAIQueries";

import type { PaymentFacts } from "./requiresPayment";

export interface OnboardingFacts extends PaymentFacts {
  /** True while either query is still settling. Facts read pessimistic. */
  loading: boolean;
  /** Re-read both facts from the server and return them. */
  refetch: () => Promise<PaymentFacts>;
}

function walletIsFunded(balanceUsdNanos: bigint | undefined): boolean {
  return balanceUsdNanos != null && BigInt(balanceUsdNanos) > 0n;
}

export function useOnboardingFacts(): OnboardingFacts {
  const eligibility = useCloudEligibility();
  const wallet = useWalletOverview();

  const eligibilityRefetch = eligibility.refetch;
  const walletRefetch = wallet.refetch;

  const refetch = useCallback(async (): Promise<PaymentFacts> => {
    const [eligibilityResult, walletResult] = await Promise.all([
      eligibilityRefetch(),
      walletRefetch(),
    ]);
    return {
      computeEligible: Boolean(eligibilityResult.data?.eligible),
      walletFunded: walletIsFunded(
        walletResult.data?.wallet?.balanceUsdNanos,
      ),
    };
  }, [eligibilityRefetch, walletRefetch]);

  const loading = eligibility.isLoading || wallet.isLoading;

  return {
    // `useCloudEligibility` already folds its own loading into `eligible`.
    computeEligible: !loading && eligibility.eligible,
    walletFunded:
      !loading && walletIsFunded(wallet.data?.wallet?.balanceUsdNanos),
    loading,
    refetch,
  };
}
