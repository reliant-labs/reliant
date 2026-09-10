/**
 * The processing fee, as the user sees it BEFORE paying.
 *
 * A surprise 5% on a card statement is a chargeback and a support ticket, so
 * the fee has to be its own line in the order summary — not folded into the
 * credit line, and not mentioned only in prose. These tests pin the disclosure
 * and, just as importantly, pin where the NUMBER comes from: the server.
 *
 * ── What is stubbed ──────────────────────────────────────────────────
 *
 * Stripe.js, and the quote RPC. The quote is stubbed as a plain value so a test
 * can hand the component a fee that does NOT equal 5% of the credit — which is
 * the only way to prove the component is rendering the server's number rather
 * than quietly recomputing one that happens to match.
 */
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("@stripe/react-stripe-js", () => ({
  Elements: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  PaymentElement: () => <div data-testid="payment-element" />,
  useStripe: () => ({ confirmPayment: vi.fn() }),
  useElements: () => ({ submit: async () => ({ error: undefined }) }),
}));

vi.mock("../stripe", () => ({
  getStripe: () => Promise.resolve({}),
  isStripeConfigured: () => true,
}));
vi.mock("../stripeAppearance", () => ({ checkoutAppearance: () => ({}) }));

/**
 * The quote the server would return. Deliberately settable per test, and
 * deliberately NOT derived from the credit amount inside the mock.
 */
let quote: {
  creditCents: bigint;
  feeCents: bigint;
  totalCents: bigint;
  feePercent: bigint;
} | null = null;

vi.mock("@/hooks/useCloudBillingQueries", () => ({
  useCreateComputeSubscriptionIntent: () => ({ mutateAsync: vi.fn() }),
  useCreateWalletTopupPaymentIntent: () => ({ mutateAsync: vi.fn() }),
  useWalletTopupQuote: () => ({ data: quote, isLoading: false }),
  isCheckoutIdentityRequired: () => false,
}));

vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));

import { OnboardingCheckout, type CheckoutLine } from "../OnboardingCheckout";

const CREDIT_LINE: CheckoutLine = {
  kind: "credit",
  label: "AI credit",
  detail: "Pays for Reliant's models.",
  amountCents: 2500,
  recurring: false,
  covered: false,
};

const COMPUTE_LINE: CheckoutLine = {
  kind: "compute",
  label: "Cloud machine",
  detail: "A Reliant-hosted machine.",
  amountCents: 2000,
  recurring: true,
  covered: false,
};

function renderCheckout(lines: CheckoutLine[], creditCents?: number) {
  return render(
    <OnboardingCheckout
      lines={lines}
      computePlanId="plan_compute"
      creditCents={creditCents}
      confirmComputeSettled={async () => true}
      confirmCreditSettled={async () => true}
      onDone={() => {}}
      onRedeemed={() => {}}
    />,
  );
}

describe("the processing fee is disclosed before payment, as its own line", () => {
  it("itemises the fee and adds it to the total", async () => {
    quote = {
      creditCents: 2500n,
      feeCents: 125n,
      totalCents: 2625n,
      feePercent: 5n,
    };
    renderCheckout([CREDIT_LINE], 2500);

    const fee = await screen.findByTestId("checkout-line-fee");
    expect(fee).toHaveTextContent("$1.25");
    // The rate is named, so the number is explicable rather than arbitrary.
    expect(fee).toHaveTextContent("5%");

    // The credit line still shows what the user actually receives. Folding the
    // fee into it would misstate the credit.
    expect(screen.getByTestId("checkout-line-credit")).toHaveTextContent("$25.00");

    // And the total is what the card will be charged.
    expect(screen.getByTestId("checkout-total")).toHaveTextContent("$26.25");
  });

  /**
   * THE TEST THAT PROVES THE FEE IS THE SERVER'S.
   *
   * The quote says $9.99 on $25.00 of credit — a number no client-side 5%
   * calculation could produce. If the component computed the fee itself, it
   * would render $1.25 here and this fails. It is the difference between
   * "displays a fee" and "displays THE fee we will charge".
   */
  it("renders the server's fee, never one it computed itself", async () => {
    quote = {
      creditCents: 2500n,
      feeCents: 999n,
      totalCents: 3499n,
      feePercent: 40n,
    };
    renderCheckout([CREDIT_LINE], 2500);

    expect(await screen.findByTestId("checkout-line-fee")).toHaveTextContent("$9.99");
    expect(screen.getByTestId("checkout-total")).toHaveTextContent("$34.99");
  });

  /**
   * The pay button must name the charged total, not the credit.
   *
   * This is the last thing a user reads before their card is charged, and it is
   * the number that has to match their statement.
   */
  it("the pay button names the charged total", async () => {
    quote = {
      creditCents: 2500n,
      feeCents: 125n,
      totalCents: 2625n,
      feePercent: 5n,
    };
    renderCheckout([CREDIT_LINE], 2500);

    await waitFor(() =>
      expect(screen.getByTestId("checkout-pay")).toHaveTextContent("$26.25"),
    );
  });

  /**
   * Compute is not fee'd, and the summary must show that rather than implying
   * the fee applies to everything.
   *
   * A $20 machine plus $25 credit is charged $46.25: the fee is 5% of the
   * CREDIT only. Rendering 5% of $45 would both overcharge and contradict the
   * price on the plan tile.
   */
  it("charges the fee on credit only, not on the compute subscription", async () => {
    quote = {
      creditCents: 2500n,
      feeCents: 125n,
      totalCents: 2625n,
      feePercent: 5n,
    };
    renderCheckout([COMPUTE_LINE, CREDIT_LINE], 2500);

    expect(await screen.findByTestId("checkout-line-fee")).toHaveTextContent("$1.25");
    // $20.00 compute + $25.00 credit + $1.25 fee.
    expect(screen.getByTestId("checkout-total")).toHaveTextContent("$46.25");
  });
});

/**
 * FOUND IN THE BROWSER, NOT IN A TEST.
 *
 * Against a server that predated the quote RPC, this page rendered a confident
 * "Due today $45.00" and a "Pay $45.00" button for a purchase the backend would
 * have charged $46.25 — the exact undisclosed surprise the fee's disclosure
 * requirement exists to prevent, produced by the disclosure feature itself.
 *
 * The failure mode is general, not specific to a stale deploy: the fee is added
 * server-side whether or not the browser managed to fetch it, so ANY failure of
 * this one query turns the page into a misquote. Failing closed costs an
 * unavailable checkout during an outage; failing open costs a chargeback, and
 * does it silently.
 */
describe("a total we cannot vouch for is never shown", () => {
  it("refuses to price the purchase when the quote is unavailable", () => {
    quote = null;
    renderCheckout([COMPUTE_LINE, CREDIT_LINE], 2500);

    // No total, and no way to pay one.
    expect(screen.queryByTestId("checkout-total")).toBeNull();
    expect(screen.queryByTestId("checkout-pay")).toBeNull();
    // And it says so, rather than silently rendering an incomplete summary.
    expect(
      screen.getByTestId("checkout-price-unavailable"),
    ).toBeInTheDocument();
  });

  /**
   * The coupon path stays open, because it is the one instrument that still
   * works when pricing does not — and it needs no quote, having no card.
   */
  it("leaves the coupon path available when pricing fails", () => {
    quote = null;
    renderCheckout([CREDIT_LINE], 2500);
    expect(screen.getByTestId("redeem-coupon")).toBeInTheDocument();
  });
});

describe("no card, no fee", () => {
  /**
   * A coupon that covers the credit leaves nothing to process, so there is
   * nothing to recover a processing cost from.
   *
   * This is the path the brief asks to verify explicitly: a fully-couponed
   * checkout must charge NOTHING — not a fee on a zero purchase.
   */
  it("shows no fee line when the credit is covered by a coupon", () => {
    quote = {
      creditCents: 2500n,
      feeCents: 125n,
      totalCents: 2625n,
      feePercent: 5n,
    };
    renderCheckout(
      [{ ...CREDIT_LINE, covered: true, coveredBy: "coupon applied" }],
      undefined,
    );

    expect(screen.queryByTestId("checkout-line-fee")).toBeNull();
    // Nothing is owed, so there is no total to charge and no card form.
    expect(screen.queryByTestId("checkout-total")).toBeNull();
    expect(screen.queryByTestId("checkout-pay")).toBeNull();
  });

  /**
   * Compute alone is not fee'd at all — no credit leg, no fee line.
   *
   * The judgement call, pinned: a recurring plan has a listed monthly price on
   * the tile, and a checkout that charged 5% on top would disagree with it.
   */
  it("shows no fee line for a compute-only purchase", async () => {
    quote = null; // nothing to quote: no credit is being bought
    renderCheckout([COMPUTE_LINE], undefined);

    await waitFor(() =>
      expect(screen.getByTestId("checkout-total")).toHaveTextContent("$20.00"),
    );
    expect(screen.queryByTestId("checkout-line-fee")).toBeNull();
  });
});
