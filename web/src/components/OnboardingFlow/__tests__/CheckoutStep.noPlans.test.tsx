/**
 * What the checkout step says when the plan catalog comes back empty.
 *
 * The old copy: "No plans are available in this setup. Go back and choose to
 * run on your own computer."
 *
 * Two things wrong with it, and the second is the expensive one:
 *
 * 1. It read as "hosted machines are unavailable here", a statement about the
 *    product. The real cause was a SERVER problem — a stale admin-server whose
 *    `planToProto` did not project `price_cents`, so every plan arrived
 *    unpriced and `isPurchasableComputePlan` (priceCents > 0) filtered them
 *    all out. The catalog was fine; the projection was missing.
 * 2. It told the user to go back and change a correct choice in order to work
 *    around our misconfiguration, and — since it replaced the checkout panel —
 *    it was the ONLY thing an unentitled user could see. A dead end phrased as
 *    advice.
 *
 * This file pins the copy honestly rather than pinning an exact sentence:
 * what matters is that it does not blame the user's choice, and that it leaves
 * the coupon path reachable, since a coupon is the one thing that still works
 * when the catalog does not.
 */
import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LaunchPlan } from "../types";

// NOT stubbed: the empty-catalog message now lives inside the compute
// checkout, alongside the coupon section that rescues it. Stubbing the
// component would stub out the very thing this file exists to pin.

vi.mock("../useCommitLaunchPlan", () => ({
  useCommitLaunchPlan: () => ({
    commit: null,
    running: false,
    runCommit: vi.fn(),
    retry: vi.fn(),
  }),
}));

vi.mock("../commitLaunchPlan", () => ({
  ensureCommitKey: vi.fn(async () => "commit-key-1"),
}));

vi.mock("../useOnboardingFacts", () => ({
  useOnboardingFacts: () => ({
    computeEligible: false,
    walletFunded: false,
    reliantBillingAvailable: true,
    loading: false,
    refetch: vi.fn(async () => ({
      computeEligible: false,
      walletFunded: false,
      reliantBillingAvailable: true,
    })),
  }),
}));

/** The stale-binary shape: plans exist, but none of them carries a price. */
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({ data: { plans: [] }, isLoading: false }),
  useCreateComputeSubscriptionIntent: () => ({ mutateAsync: vi.fn() }),
  useCreateWalletTopupPaymentIntent: () => ({ mutateAsync: vi.fn() }),
  useWalletTopupQuote: () => ({ data: undefined, isLoading: false }),
  isCheckoutIdentityRequired: () => false,
}));

vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

import { CheckoutStep } from "../steps/CheckoutStep";

const CLOUD_OWN_KEY: Partial<LaunchPlan> = {
  compute: "cloud_paid",
  modelProvider: "anthropic",
};

function renderStep() {
  return render(
    <CheckoutStep
      plan={CLOUD_OWN_KEY as LaunchPlan}
      updatePlan={vi.fn()}
      onNext={vi.fn()}
      onBack={vi.fn()}
    />,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("CheckoutStep — empty plan catalog", () => {
  it("does not tell the user to go back and change their choice", () => {
    renderStep();
    expect(
      screen.queryByText(/go back and choose to run on your own computer/i),
    ).toBeNull();
  });

  // It should read as "something is wrong on our end", which is what is
  // actually true, rather than as a property of the product the user picked.
  it("says the problem is ours, not the user's setup", () => {
    renderStep();
    expect(screen.getByTestId("checkout-plans-unavailable")).toHaveTextContent(
      /couldn't load|can't load|unavailable right now|try again/i,
    );
  });

  // The coupon is the one path that still works with no purchasable plan, so
  // the empty state must not be the end of the road.
  it("still offers coupon redemption", () => {
    renderStep();
    expect(screen.getByTestId("redeem-coupon")).toBeInTheDocument();
  });
});
