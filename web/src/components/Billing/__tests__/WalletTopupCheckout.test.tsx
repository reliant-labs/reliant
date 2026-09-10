/**
 * Our own top-up page.
 *
 * ── What is under test, and what deliberately is not ──────────────────
 *
 * Not Stripe. `@stripe/react-stripe-js` is stubbed: `<PaymentElement>` renders
 * cross-origin iframes that jsdom cannot mount, and asserting on Stripe's own
 * fields would be testing their product. What IS pinned is the contract
 * between this page and Stripe — what we do with each `confirmPayment`
 * outcome — because that is where a user ends up believing they paid when they
 * did not.
 *
 * The rule these tests exist to defend: `confirmPayment` returning `succeeded`
 * is a PRESENTATION signal. Entitlement comes from the webhook, so success is
 * claimed only after `confirmSettlement` — a read of the SERVER — agrees.
 */
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

// ── Mocks ────────────────────────────────────────────────────────────────

const mockConfirmPayment = vi.fn();

vi.mock("@stripe/react-stripe-js", () => ({
  // Renders children directly: the provider's only job here is supplying the
  // stripe/elements objects, which the two hooks below stub.
  Elements: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  PaymentElement: () => <div data-testid="payment-element" />,
  useStripe: () => ({ confirmPayment: mockConfirmPayment }),
  useElements: () => ({}),
}));

vi.mock("../stripe", () => ({
  getStripe: () => Promise.resolve({}),
  isStripeConfigured: () => true,
}));

const mockIntentState = {
  current: {
    status: "ready" as const,
    clientSecret: "pi_test_secret",
    topupId: "topup-1",
  },
};
vi.mock("../useWalletTopupIntent", () => ({
  useWalletTopupIntent: () => mockIntentState.current,
}));

/**
 * The fee quote, stubbed as a plain value.
 *
 * The real hook is a react-query call, and this suite mounts the component
 * without a QueryClientProvider — so it is mocked here for the same reason
 * `useWalletTopupIntent` above is. The quote's own arithmetic belongs to the
 * server and is pinned in control-plane's processing_fee_test.go; the fee's
 * DISPLAY is pinned in WalletTopupCheckout.fee.test.tsx.
 */
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  useWalletTopupQuote: (creditCents: number) => ({
    data: {
      creditCents: BigInt(creditCents),
      feeCents: BigInt(Math.floor((creditCents * 5) / 100)),
      totalCents: BigInt(creditCents + Math.floor((creditCents * 5) / 100)),
      feePercent: 5n,
    },
    isLoading: false,
  }),
}));

vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: ({
    onRedeemed,
  }: {
    onRedeemed?: (r: unknown) => void;
  }) => (
    <button
      type="button"
      data-testid="redeem"
      onClick={() => onRedeemed?.({ kind: 1, amountCents: 2000 })}
    >
      redeem
    </button>
  ),
}));

import { WalletTopupCheckout } from "../WalletTopupCheckout";

// ── Harness ──────────────────────────────────────────────────────────────

function renderPage(overrides: {
  confirmSettlement?: () => Promise<boolean>;
  onDone?: () => void;
} = {}) {
  const onDone = overrides.onDone ?? vi.fn();
  const confirmSettlement =
    overrides.confirmSettlement ?? vi.fn(async () => true);
  render(
    <WalletTopupCheckout
      confirmSettlement={confirmSettlement}
      onDone={onDone}
    />,
  );
  return { onDone, confirmSettlement };
}

beforeEach(() => {
  vi.clearAllMocks();
  mockIntentState.current = {
    status: "ready",
    clientSecret: "pi_test_secret",
    topupId: "topup-1",
  };
});

// ── Tests ────────────────────────────────────────────────────────────────

describe("WalletTopupCheckout — the page is ours", () => {
  it("renders our own amount selector and submit button, not Stripe's", () => {
    renderPage();
    // Our controls, around Stripe's card fields.
    //
    // The picker offers CREDIT ($25.00 — what lands in the balance) while the
    // button names the CHARGE ($26.25 — credit plus the processing fee). The
    // two deliberately differ, and the button carries the larger one because it
    // is the last number read before paying and the one that reaches the card
    // statement.
    expect(
      screen.getByRole("button", { name: /^\$25\.00$/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /^Pay \$26\.25$/ }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("payment-element")).toBeInTheDocument();
  });

  it("puts coupon redemption on the page as a way to pay, not behind a link", () => {
    renderPage();
    // Visible without disclosure: for many early users a code IS the payment,
    // and Stripe's own promo field cannot redeem our grants at all.
    expect(screen.getByTestId("redeem")).toBeVisible();
  });

  it("shows a covered leg rather than hiding it, so a coupon's value stays visible", () => {
    render(
      <WalletTopupCheckout
        confirmSettlement={vi.fn(async () => false)}
        onDone={vi.fn()}
        legs={[
          {
            label: "Cloud machine",
            detail: "A Reliant-hosted machine.",
            covered: true,
            coveredBy: "coupon applied",
          },
          {
            label: "AI credit",
            detail: "Pays for Reliant's models.",
            covered: false,
          },
        ]}
      />,
    );
    expect(screen.getByText("Cloud machine")).toBeInTheDocument();
    expect(screen.getByText("coupon applied")).toBeInTheDocument();
    expect(screen.getByText("AI credit")).toBeInTheDocument();
  });
});

describe("WalletTopupCheckout — what happens after Pay", () => {
  it("does NOT claim success until the server confirms the credit landed", async () => {
    const user = userEvent.setup();
    mockConfirmPayment.mockResolvedValue({
      paymentIntent: { status: "succeeded" },
    });
    // The webhook has not landed yet.
    const confirmSettlement = vi.fn(async () => false);
    const { onDone } = renderPage({ confirmSettlement });

    await user.click(screen.getByRole("button", { name: /^Pay/ }));

    await waitFor(() =>
      expect(screen.getByText(/confirming your payment/i)).toBeVisible(),
    );
    // Stripe said yes; our server has not. Nothing is claimed and the caller
    // is not told the purchase completed.
    expect(onDone).not.toHaveBeenCalled();
    expect(confirmSettlement).toHaveBeenCalled();
  });

  it("reports done once the server agrees", async () => {
    const user = userEvent.setup();
    mockConfirmPayment.mockResolvedValue({
      paymentIntent: { status: "succeeded" },
    });
    const { onDone } = renderPage({
      confirmSettlement: vi.fn(async () => true),
    });

    await user.click(screen.getByRole("button", { name: /^Pay/ }));

    await waitFor(() => expect(onDone).toHaveBeenCalled());
  });

  /**
   * The 3DS case, and the reason it gets its own state.
   *
   * A challenge that fails silently leaves a user certain they paid. So
   * `requires_action` must produce a distinct, explicit message — not the
   * generic decline copy, and above all not the success path.
   */
  it("says explicitly when a 3DS challenge did not complete", async () => {
    const user = userEvent.setup();
    mockConfirmPayment.mockResolvedValue({
      paymentIntent: { status: "requires_action" },
    });
    const { onDone } = renderPage();

    await user.click(screen.getByRole("button", { name: /^Pay/ }));

    await waitFor(() =>
      expect(screen.getByText(/bank needs to verify/i)).toBeVisible(),
    );
    // And it tells the user where they stand on money, which is the actual
    // question: nothing was taken.
    expect(screen.getByText(/nothing has been charged/i)).toBeVisible();
    expect(onDone).not.toHaveBeenCalled();
  });

  it("shows a declined card's own message, and does not report done", async () => {
    const user = userEvent.setup();
    mockConfirmPayment.mockResolvedValue({
      error: { type: "card_error", message: "Your card was declined." },
    });
    const { onDone } = renderPage();

    await user.click(screen.getByRole("button", { name: /^Pay/ }));

    await waitFor(() =>
      expect(screen.getByText("Your card was declined.")).toBeVisible(),
    );
    expect(onDone).not.toHaveBeenCalled();
  });

  it("does not show an integration error's raw message to the user", async () => {
    const user = userEvent.setup();
    mockConfirmPayment.mockResolvedValue({
      error: { type: "api_error", message: "No such payment_intent: pi_x" },
    });
    renderPage();

    await user.click(screen.getByRole("button", { name: /^Pay/ }));

    await waitFor(() =>
      expect(screen.getByText(/couldn't process that payment/i)).toBeVisible(),
    );
    expect(screen.queryByText(/No such payment_intent/)).toBeNull();
  });

  it("treats a redeemed coupon exactly like a payment — server first", async () => {
    const user = userEvent.setup();
    // The redemption reported success but the wallet did not move.
    const confirmSettlement = vi.fn(async () => false);
    const { onDone } = renderPage({ confirmSettlement });

    await user.click(screen.getByTestId("redeem"));

    await waitFor(() => expect(confirmSettlement).toHaveBeenCalled());
    expect(onDone).not.toHaveBeenCalled();
  });
});

describe("WalletTopupCheckout — before the card form can mount", () => {
  it("surfaces an intent failure instead of a blank card area", () => {
    mockIntentState.current = {
      status: "error",
      message: "Top-ups are unavailable right now.",
    } as never;
    renderPage();
    expect(
      screen.getByText("Top-ups are unavailable right now."),
    ).toBeVisible();
  });
});
