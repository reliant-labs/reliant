/**
 * The checkout panel must never mount Stripe Elements for a $0 bill.
 *
 * ── The crash ─────────────────────────────────────────────────────────
 *
 * `<Elements>` is constructed with `options.amount`, and Stripe.js validates
 * that eagerly: a non-positive amount throws
 *
 *   IntegrationError: Invalid value for elements(): `amount` must be greater than 0
 *
 * out of the render, past every local error boundary, and the whole app is
 * replaced by "Something went wrong". The user's onboarding is over — there is
 * no Back, no coupon field, nothing. It is the single most destructive failure
 * in the flow, and it fires on the HAPPIEST path: someone whose coupon covered
 * everything.
 *
 * ── Why this is a real user state and not a hand-edited URL ───────────
 *
 * `covered` is computed per leg from `requiresPayment`, but the AMOUNT on a
 * covered line comes from the catalog. A compute line the user is not being
 * charged for still carries `amountCents`, and a line whose plan could not be
 * priced carries 0. So `owed` can be non-empty — the gate at the top of the
 * component opens — while `totalCents` sums to 0. That is exactly the
 * unpriced-catalog state the sibling `computeUnsellable` message was written
 * for, and it reaches `<Elements>` on the same render.
 *
 * The guard is therefore on the AMOUNT, at the mount, rather than on any of
 * the conditions that happen to produce it. A card form that cannot charge
 * anything has nothing to collect.
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import {
  OnboardingCheckout,
  type CheckoutLine,
} from "@/components/Billing/OnboardingCheckout";

// The real Stripe Elements provider throws on a non-positive amount. This
// stub reproduces THAT rule and nothing else, so the test fails for the same
// reason production did rather than for a mock's own opinion.
vi.mock("@stripe/react-stripe-js", () => ({
  Elements: ({
    options,
    children,
  }: {
    options: { amount?: number };
    children: React.ReactNode;
  }) => {
    if (!options?.amount || options.amount <= 0) {
      throw new Error(
        "IntegrationError: Invalid value for elements(): `amount` must be greater than 0",
      );
    }
    return <div data-testid="stripe-elements">{children}</div>;
  },
  PaymentElement: () => <div data-testid="payment-element" />,
  useElements: () => null,
  useStripe: () => null,
}));

vi.mock("@/components/Billing/stripe", () => ({
  getStripe: () => ({}),
  isStripeConfigured: () => true,
}));

vi.mock("@/hooks/useCloudBillingQueries", () => ({
  useCreateComputeSubscriptionIntent: () => ({ mutateAsync: vi.fn() }),
  useCreateWalletTopupPaymentIntent: () => ({ mutateAsync: vi.fn() }),
  // Nothing is owed in these tests, so nothing is quotable — which is exactly
  // the shape the real hook returns when it is disabled.
  useWalletTopupQuote: () => ({ data: undefined, isLoading: false }),
  isCheckoutIdentityRequired: () => false,
}));

vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));

function renderCheckout(lines: CheckoutLine[], computePlanId?: string) {
  return render(
    <OnboardingCheckout
      lines={lines}
      computePlanId={computePlanId}
      confirmComputeSettled={vi.fn(async () => true)}
      confirmCreditSettled={vi.fn(async () => true)}
      onDone={vi.fn()}
      onRedeemed={vi.fn()}
    />,
  );
}

describe("OnboardingCheckout — nothing actually chargeable", () => {
  // Both legs covered by coupons: the owner's both-coupons path. Nothing is
  // owed, so there is no card form to render at all.
  it("renders no card form when every leg is covered", () => {
    renderCheckout([
      {
        kind: "compute",
        label: "Cloud machine",
        detail: "A Reliant-hosted machine.",
        amountCents: 2000,
        recurring: true,
        covered: true,
        coveredBy: "coupon",
      },
      {
        kind: "credit",
        label: "AI credit",
        detail: "Pays for Reliant's models.",
        amountCents: 2000,
        recurring: false,
        covered: true,
        coveredBy: "coupon",
      },
    ]);

    expect(screen.queryByTestId("stripe-elements")).toBeNull();
    expect(screen.queryByTestId("checkout-pay")).toBeNull();
    // The coupon section stays: it is what covered the bill in the first place.
    expect(screen.getByTestId("redeem-coupon")).toBeInTheDocument();
  });

  // The crash itself. An owed line whose amount is 0 — the unpriced-catalog
  // shape — must not reach Elements.
  it("does not construct Elements for a zero total", () => {
    expect(() =>
      renderCheckout(
        [
          {
            kind: "compute",
            label: "Cloud machine",
            detail: "A Reliant-hosted machine.",
            // The catalog could not price this plan.
            amountCents: 0,
            recurring: true,
            covered: false,
          },
        ],
        "plan_compute_small",
      ),
    ).not.toThrow();

    expect(screen.queryByTestId("stripe-elements")).toBeNull();
  });

  // And it must SAY something rather than rendering an empty panel: a user
  // looking at a blank "Pay with card" section has no idea what to do next.
  it("explains itself instead of showing an empty pay section", () => {
    renderCheckout(
      [
        {
          kind: "compute",
          label: "Cloud machine",
          detail: "A Reliant-hosted machine.",
          amountCents: 0,
          recurring: true,
          covered: false,
        },
      ],
      "plan_compute_small",
    );

    expect(
      screen.getByTestId("checkout-plans-unavailable"),
    ).toBeInTheDocument();
  });

  // A genuinely chargeable bill still mounts the form — the guard must not
  // have turned the card off for everyone.
  it("still mounts the card form when there is something to charge", () => {
    renderCheckout(
      [
        {
          kind: "compute",
          label: "Cloud machine",
          detail: "A Reliant-hosted machine.",
          amountCents: 2000,
          recurring: true,
          covered: false,
        },
      ],
      "plan_compute_small",
    );

    expect(screen.getByTestId("stripe-elements")).toBeInTheDocument();
  });
});
