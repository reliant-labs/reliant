/**
 * The single-card checkout: one card entry, two purchases, and what happens
 * when only one of them works.
 *
 * ── What is stubbed, and why that is honest ───────────────────────────
 *
 * Stripe.js is stubbed at the module boundary — `useStripe` / `useElements` /
 * `<PaymentElement>` — because the real ones require a network-loaded script
 * and a live publishable key. What is NOT stubbed is the component's own
 * decision-making: which intents it creates, in what order, what it does with
 * the payment method the first confirmation returns, and what it does when the
 * second leg declines. That is the whole of the logic this file exists to pin.
 *
 * The stubbed `confirmPayment` is driven per-test, so a test can say "the
 * subscription succeeds and the top-up declines" and assert on exactly the
 * behaviour that follows.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

// ── Stripe.js, stubbed ───────────────────────────────────────────────────

/** Queued results for successive `confirmPayment` calls. */
let confirmResults: unknown[] = [];
/** Every `confirmPayment` argument object, in order. */
const confirmCalls: Record<string, unknown>[] = [];

const mockConfirmPayment = vi.fn(async (opts: Record<string, unknown>) => {
  confirmCalls.push(opts);
  return (
    confirmResults.shift() ?? {
      paymentIntent: { status: "succeeded", payment_method: "pm_default" },
    }
  );
});

vi.mock("@stripe/react-stripe-js", () => ({
  Elements: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  PaymentElement: () => <div data-testid="payment-element" />,
  useStripe: () => ({ confirmPayment: mockConfirmPayment }),
  useElements: () => ({ submit: async () => ({ error: undefined }) }),
}));

vi.mock("../stripe", () => ({
  getStripe: () => Promise.resolve({}),
  isStripeConfigured: () => true,
}));

vi.mock("../stripeAppearance", () => ({ checkoutAppearance: () => ({}) }));

// ── The two intent-creating RPCs ─────────────────────────────────────────

const mockComputeIntent = vi.fn(async (_planId: string) => ({
  paymentClientSecret: "seti_compute_secret",
}));
const mockTopupIntent = vi.fn(async (_cents: bigint) => ({
  paymentIntentClientSecret: "pi_topup_secret",
}));

vi.mock("@/hooks/useCloudBillingQueries", () => ({
  useCreateComputeSubscriptionIntent: () => ({
    mutateAsync: mockComputeIntent,
  }),
  useCreateWalletTopupPaymentIntent: () => ({ mutateAsync: mockTopupIntent }),
  /**
   * A zero-fee quote, so the totals these tests assert on stay the plain sums
   * of their line items — this suite is about the two-leg payment mechanics,
   * not about the fee.
   *
   * It must return a QUOTE rather than `undefined`: an absent quote now means
   * "we could not price this", which correctly blocks the pay button. The
   * fee's own arithmetic and that fail-closed behaviour are both pinned in
   * OnboardingCheckout.fee.test.tsx.
   */
  useWalletTopupQuote: (creditCents: number) => ({
    data: {
      creditCents: BigInt(creditCents),
      feeCents: 0n,
      totalCents: BigInt(creditCents),
      feePercent: 0n,
    },
    isLoading: false,
  }),
  isCheckoutIdentityRequired: (err: unknown) =>
    err instanceof Error && err.message.includes("identity"),
}));

vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));

import { OnboardingCheckout, type CheckoutLine } from "../OnboardingCheckout";

// ── Harness ──────────────────────────────────────────────────────────────

const COMPUTE_LINE: CheckoutLine = {
  kind: "compute",
  label: "Cloud machine",
  detail: "A Reliant-hosted machine.",
  amountCents: 2000,
  recurring: true,
  covered: false,
};

const CREDIT_LINE: CheckoutLine = {
  kind: "credit",
  label: "AI credit",
  detail: "Pays for Reliant's models.",
  amountCents: 2500,
  recurring: false,
  covered: false,
};

let confirmCompute: () => Promise<boolean>;
let confirmCredit: () => Promise<boolean>;
let onDone: ReturnType<typeof vi.fn>;

function renderCheckout(lines: CheckoutLine[], overrides: Partial<{
  computePlanId: string;
  creditCents: number;
}> = {}) {
  const owedCompute = lines.some((l) => l.kind === "compute" && !l.covered);
  const owedCredit = lines.some((l) => l.kind === "credit" && !l.covered);
  return render(
    <OnboardingCheckout
      lines={lines}
      computePlanId={
        owedCompute
          ? (overrides.computePlanId ?? "plan_compute_small")
          : undefined
      }
      creditCents={owedCredit ? (overrides.creditCents ?? 2500) : undefined}
      confirmComputeSettled={() => confirmCompute()}
      confirmCreditSettled={() => confirmCredit()}
      onDone={onDone}
      onRedeemed={vi.fn()}
    />,
  );
}

async function pay() {
  await act(async () => {
    fireEvent.click(screen.getByTestId("checkout-pay"));
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  confirmResults = [];
  confirmCalls.length = 0;
  confirmCompute = async () => true;
  confirmCredit = async () => true;
  onDone = vi.fn();
});

// ── The summary ──────────────────────────────────────────────────────────

describe("OnboardingCheckout — the summary", () => {
  // A coupon that removes a line from the page reads as though it did nothing,
  // at exactly the moment the user is being asked for money.
  it("shows a covered leg, marked, rather than hiding it", () => {
    renderCheckout([
      { ...COMPUTE_LINE, covered: true, coveredBy: "coupon applied" },
      CREDIT_LINE,
    ]);
    expect(screen.getByTestId("checkout-line-compute")).toHaveTextContent(
      /coupon applied/i,
    );
    // And the total counts only what is still owed, so the coupon visibly
    // reduces it.
    expect(screen.getByTestId("checkout-total")).toHaveTextContent("$25.00");
  });

  it("totals both legs when both are owed", () => {
    renderCheckout([COMPUTE_LINE, CREDIT_LINE]);
    expect(screen.getByTestId("checkout-total")).toHaveTextContent("$45.00");
  });

  // THE OWNER'S STATED PRIORITY, at the component level: nothing owed means no
  // card form at all — not a disabled one, not an empty one.
  it("renders no card form when every leg is covered", () => {
    renderCheckout([
      { ...COMPUTE_LINE, covered: true, coveredBy: "coupon applied" },
      { ...CREDIT_LINE, covered: true, coveredBy: "coupon applied" },
    ]);
    expect(screen.queryByTestId("payment-element")).toBeNull();
    expect(screen.queryByTestId("checkout-pay")).toBeNull();
    // The coupon field stays: a user may still be holding a code.
    expect(screen.getByTestId("redeem-coupon")).toBeInTheDocument();
  });
});

// ── One card, two purchases ──────────────────────────────────────────────

describe("OnboardingCheckout — one card entry", () => {
  // THE CONSOLIDATION. One `<PaymentElement>` is mounted for both purchases,
  // and the second leg is confirmed against the payment method the first one
  // returned rather than by collecting a card again.
  it("pays the second leg with the card the first leg collected", async () => {
    confirmResults = [
      { paymentIntent: { status: "succeeded", payment_method: "pm_from_card" } },
      { paymentIntent: { status: "succeeded" } },
    ];
    renderCheckout([COMPUTE_LINE, CREDIT_LINE]);

    // Exactly one card form for the whole purchase.
    expect(screen.getAllByTestId("payment-element")).toHaveLength(1);

    await pay();

    await waitFor(() => expect(confirmCalls).toHaveLength(2));
    // Leg 1 collects the card through Elements.
    expect(confirmCalls[0].elements).toBeDefined();
    // Leg 2 reuses the collected method and mounts no Elements of its own —
    // this is what "one card entry" means mechanically.
    expect(confirmCalls[1].elements).toBeUndefined();
    expect(
      (confirmCalls[1].confirmParams as Record<string, unknown>).payment_method,
    ).toBe("pm_from_card");
    expect(onDone).toHaveBeenCalled();
  });

  it("collects the card through Elements when credit is the only leg", async () => {
    renderCheckout([CREDIT_LINE]);
    await pay();

    await waitFor(() => expect(confirmCalls).toHaveLength(1));
    expect(confirmCalls[0].elements).toBeDefined();
    expect(mockComputeIntent).not.toHaveBeenCalled();
    expect(mockTopupIntent).toHaveBeenCalledWith(BigInt(2500));
  });

  // Compute is paid FIRST, deliberately: it is the leg that collects the card,
  // and it is the larger recurring commitment the user's machine depends on.
  it("pays compute before credit", async () => {
    renderCheckout([COMPUTE_LINE, CREDIT_LINE]);
    await pay();

    await waitFor(() => expect(confirmCalls).toHaveLength(2));
    expect(confirmCalls[0].clientSecret).toBe("seti_compute_secret");
    expect(confirmCalls[1].clientSecret).toBe("pi_topup_secret");
  });

  // A covered leg is not re-bought. The summary still lists it; the card does
  // not pay for it.
  it("charges only the legs that are still owed", async () => {
    renderCheckout([
      { ...COMPUTE_LINE, covered: true, coveredBy: "coupon applied" },
      CREDIT_LINE,
    ]);
    await pay();

    await waitFor(() => expect(mockTopupIntent).toHaveBeenCalled());
    expect(mockComputeIntent).not.toHaveBeenCalled();
  });
});

// ── Partial failure ──────────────────────────────────────────────────────

describe("OnboardingCheckout — partial failure", () => {
  /**
   * THE HARD CASE. The subscription succeeds and the top-up declines.
   *
   * The user must be told BOTH facts — that the machine is paid for, and that
   * the credit is not — because a bare "payment failed" invites them to
   * re-enter a card for something they already bought.
   */
  it("says what succeeded when the second leg declines", async () => {
    confirmResults = [
      { paymentIntent: { status: "succeeded", payment_method: "pm_x" } },
      { error: { type: "card_error", message: "Your card was declined." } },
    ];
    renderCheckout([COMPUTE_LINE, CREDIT_LINE]);
    await pay();

    await waitFor(() => {
      expect(screen.getByText(/your machine is paid for/i)).toBeInTheDocument();
    });
    expect(screen.getByText(/card was declined/i)).toBeInTheDocument();
    // The flow does NOT advance: credit is still owed.
    expect(onDone).not.toHaveBeenCalled();
  });

  /**
   * THE DOUBLE-CHARGE GUARD, client side.
   *
   * After a half-failure, pressing Pay again must re-attempt ONLY the leg that
   * failed. A settled leg is recorded per-leg precisely so a retry cannot
   * charge it twice.
   */
  it("does not re-charge a settled leg on retry", async () => {
    confirmResults = [
      { paymentIntent: { status: "succeeded", payment_method: "pm_x" } },
      { error: { type: "card_error", message: "Your card was declined." } },
    ];
    renderCheckout([COMPUTE_LINE, CREDIT_LINE]);
    await pay();
    await waitFor(() =>
      expect(screen.getByText(/your machine is paid for/i)).toBeInTheDocument(),
    );

    expect(mockComputeIntent).toHaveBeenCalledTimes(1);

    // Retry: the credit leg succeeds this time.
    confirmResults = [{ paymentIntent: { status: "succeeded" } }];
    await pay();

    await waitFor(() => expect(onDone).toHaveBeenCalled());
    // The compute leg was never asked for a second time — no second
    // subscription, no second charge.
    expect(mockComputeIntent).toHaveBeenCalledTimes(1);
    expect(mockTopupIntent).toHaveBeenCalledTimes(2);
  });

  /**
   * A webhook that is merely LATE is not a failure, and must not abandon the
   * second leg: the card demonstrably works, so the credit can still be bought
   * while the subscription catches up.
   */
  it("still buys the credit when the compute webhook is slow", async () => {
    vi.useFakeTimers();
    try {
      confirmCompute = async () => false; // never confirms
      renderCheckout([COMPUTE_LINE, CREDIT_LINE]);

      fireEvent.click(screen.getByTestId("checkout-pay"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(70_000);
      });

      // Both legs were paid for despite the first one not confirming.
      expect(mockComputeIntent).toHaveBeenCalledTimes(1);
      expect(mockTopupIntent).toHaveBeenCalledTimes(1);
      // And it says so honestly rather than claiming success.
      expect(
        screen.getByText(/haven't been able to confirm/i),
      ).toBeInTheDocument();
      expect(onDone).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  // A failure to CREATE the first intent charges nothing and must not go on to
  // charge the second leg either — there is no card collected yet.
  it("stops before the second leg when the first intent cannot be created", async () => {
    mockComputeIntent.mockRejectedValueOnce(new Error("[internal] stripe down"));
    renderCheckout([COMPUTE_LINE, CREDIT_LINE]);
    await pay();

    await waitFor(() =>
      expect(screen.getByText(/stripe down/i)).toBeInTheDocument(),
    );
    expect(mockTopupIntent).not.toHaveBeenCalled();
    expect(onDone).not.toHaveBeenCalled();
  });
});

// ── 3DS ──────────────────────────────────────────────────────────────────

describe("OnboardingCheckout — authentication", () => {
  /**
   * A 3DS challenge that did not complete must be SAID, never swallowed. A
   * silent failure here leaves a user certain they paid — the worst outcome on
   * this page.
   */
  it("surfaces an unfinished 3DS challenge rather than failing silently", async () => {
    confirmResults = [{ paymentIntent: { status: "requires_action" } }];
    renderCheckout([COMPUTE_LINE, CREDIT_LINE]);
    await pay();

    await waitFor(() =>
      expect(screen.getByText(/bank needs to verify/i)).toBeInTheDocument(),
    );
    // Nothing was charged, so the second leg must not have been attempted.
    expect(mockTopupIntent).not.toHaveBeenCalled();
    expect(onDone).not.toHaveBeenCalled();
  });

  // 3DS on the SECOND leg is reachable too, because that leg is confirmed
  // on-session. This is the reason both legs confirm with the user present
  // rather than charging the saved card off-session, where no challenge can be
  // answered at all.
  it("surfaces a challenge on the reused-card leg", async () => {
    confirmResults = [
      { paymentIntent: { status: "succeeded", payment_method: "pm_x" } },
      { paymentIntent: { status: "requires_action" } },
    ];
    renderCheckout([COMPUTE_LINE, CREDIT_LINE]);
    await pay();

    await waitFor(() =>
      expect(screen.getByText(/bank needs to verify/i)).toBeInTheDocument(),
    );
    expect(onDone).not.toHaveBeenCalled();
  });
});
