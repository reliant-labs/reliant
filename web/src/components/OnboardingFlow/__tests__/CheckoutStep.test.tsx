/**
 * The consolidated checkout step.
 *
 * ── What is actually under test ───────────────────────────────────────
 *
 * Not Stripe. Each checkout component is stubbed, because it owns intent
 * creation, the anonymous-user refusal and the server-confirmation poll, and
 * it has its own tests. What this file pins is everything AROUND the panel —
 * the decisions the step makes about money and provisioning:
 *
 *   1. The commit fires from the confirmation handler, ONCE, and never from an
 *      effect. That rule is why a speculative `CreateDaemon` was removed from
 *      `ComputeStep`; a payment screen is the last place to reintroduce it.
 *   2. Nothing is claimed before the SERVER agrees. `paid` is written only
 *      after the facts have been re-read and show the debt cleared.
 *   3. A user owing both compute and credit pays for both without leaving.
 *
 * The panel stub exposes a button that calls `onDone`, which is exactly the
 * contract the real panel offers: "the server has confirmed this purchase."
 * Driving the step through that button is driving it through the only entry
 * point a real payment has.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LaunchPlan } from "../types";
import type { PaymentFacts } from "../requiresPayment";

// ── Mocks ────────────────────────────────────────────────────────────────

/**
 * What `onDone` means: the server confirmed it. Nothing else calls it.
 *
 * ONE checkout is stubbed now, not two. That is the change under test: the
 * step used to mount `ComputeSubscriptionCheckout` and then
 * `WalletTopupCheckout` in sequence — two card forms — and now mounts a single
 * `OnboardingCheckout` that takes one card for every leg still owed.
 *
 * It is stubbed for the reason the panels always were: it owns intent
 * creation, the anonymous-user refusal, the 3DS handling and the confirmation
 * poll, and it has its own tests. What this file pins is everything AROUND it.
 *
 * The stub records the LINES it was asked to render, so the assertions read as
 * "what did this step ask to buy?" rather than "which component did it mount?"
 * — which is what let this file survive the compute leg moving from embedded
 * Checkout, to Elements, and now to a single consolidated form.
 */
interface StubLine {
  kind: string;
  amountCents: number;
  covered: boolean;
}
const mockPanelRequests: {
  lines: StubLine[];
  computePlanId?: string;
  creditCents?: number;
}[] = [];

vi.mock("@/components/Billing/OnboardingCheckout", () => ({
  OnboardingCheckout: ({
    lines,
    computePlanId,
    creditCents,
    onDone,
  }: {
    lines: StubLine[];
    computePlanId?: string;
    creditCents?: number;
    onDone: () => void;
  }) => {
    mockPanelRequests.push({ lines, computePlanId, creditCents });
    return (
      <div>
        {/* The summary is the step's own output, so the stub renders enough of
            it for the "what is on the page" assertions to be about the step. */}
        {lines.map((line) => (
          <div key={line.kind} data-testid={`stub-line-${line.kind}`}>
            {line.kind} {line.amountCents} {line.covered ? "covered" : "owed"}
          </div>
        ))}
        <button type="button" data-testid="confirm-payment" onClick={onDone}>
          confirm
        </button>
      </div>
    );
  },
}));

/** The lines the most recent render asked for, keyed by leg. */
function lastLine(kind: string): StubLine | undefined {
  return mockPanelRequests.at(-1)?.lines.find((l) => l.kind === kind);
}

const mockRunCommit = vi.fn(async () => ({
  commitKey: "k",
  status: "complete" as const,
  tasks: [],
}));
vi.mock("../useCommitLaunchPlan", () => ({
  useCommitLaunchPlan: () => ({
    commit: null,
    running: false,
    runCommit: mockRunCommit,
    retry: vi.fn(),
  }),
}));

// The step must not mint a commit key by navigating; the harness supplies one.
vi.mock("../commitLaunchPlan", () => ({
  ensureCommitKey: vi.fn(async () => "commit-key-1"),
}));

/**
 * Facts, driven per-test. `factsRefetch` is what the step polls after a
 * confirmation, so a test controls exactly when the server "agrees".
 */
/**
 * The entitlement facts, driven per-test.
 *
 * `reliantBillingAvailable` is layered in here rather than written into every
 * assignment below: it is a deployment constant, true wherever this step can
 * render at all, and it is the entitlement pair that each test is actually
 * varying. A test that owed money only because a build flag was undefined
 * would be testing the harness.
 */
type EntitlementFacts = Omit<PaymentFacts, "reliantBillingAvailable">;

let currentFacts: EntitlementFacts = {
  computeEligible: false,
  walletFunded: false,
};
const factsWithBilling = (): PaymentFacts => ({
  ...currentFacts,
  reliantBillingAvailable: true,
});
const mockFactsRefetch = vi.fn(async () => factsWithBilling());
vi.mock("../useOnboardingFacts", () => ({
  useOnboardingFacts: () => ({
    ...factsWithBilling(),
    loading: false,
    refetch: mockFactsRefetch,
  }),
}));

const PLAN_SMALL = {
  id: "plan_compute_small",
  priceCents: 2000n,
  displayOrder: 1,
  structuredLimits: {
    allowedDaemonSizes: ["small"],
    daemonComputeIncludedMinutes: 960,
    daemonOveragePerMinuteCents: 2,
  },
};

vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({ data: { plans: [PLAN_SMALL] }, isLoading: false }),
}));

vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

import { CheckoutStep } from "../steps/CheckoutStep";

// ── Harness ──────────────────────────────────────────────────────────────

function renderStep(
  plan: Partial<LaunchPlan>,
  overrides: {
    updatePlan?: (u: Partial<LaunchPlan>) => void;
    onNext?: () => void;
  } = {},
) {
  return render(
    <CheckoutStep
      plan={plan as LaunchPlan}
      updatePlan={overrides.updatePlan ?? vi.fn()}
      onNext={overrides.onNext ?? vi.fn()}
      onBack={vi.fn()}
    />,
  );
}

const CLOUD_OWN_KEY: Partial<LaunchPlan> = {
  compute: "cloud_paid",
  modelProvider: "anthropic",
  computePlanId: "plan_compute_small",
};

const LOCAL_RELIANT: Partial<LaunchPlan> = {
  compute: "local_daemon",
  modelProvider: "reliant_credits",
};

const CLOUD_AND_RELIANT: Partial<LaunchPlan> = {
  compute: "cloud_paid",
  modelProvider: "reliant_credits",
  computePlanId: "plan_compute_small",
};

beforeEach(() => {
  vi.clearAllMocks();
  mockPanelRequests.length = 0;
  currentFacts = { computeEligible: false, walletFunded: false };
});

// ── Tests ────────────────────────────────────────────────────────────────

describe("CheckoutStep — what it asks for", () => {
  it("buys a compute subscription when only compute is owed", () => {
    renderStep(CLOUD_OWN_KEY);
    expect(mockPanelRequests.at(-1)?.computePlanId).toBe("plan_compute_small");
    expect(lastLine("compute")).toMatchObject({ covered: false });
    // Own API key: no credit line at all, because nothing about this plan buys
    // Reliant's models.
    expect(lastLine("credit")).toBeUndefined();
  });

  it("buys wallet credit when only credit is owed", () => {
    currentFacts = { computeEligible: true, walletFunded: false };
    renderStep(LOCAL_RELIANT);
    expect(lastLine("credit")).toMatchObject({ covered: false });
    expect(mockPanelRequests.at(-1)?.creditCents).toBeGreaterThan(0);
    // Local compute is free, so there is no machine to pay for.
    expect(lastLine("compute")).toBeUndefined();
  });

  // ── THE CONSOLIDATION, stated as a test ──────────────────────────────
  //
  // This used to assert the opposite — "starts with compute … and labels it as
  // 1 of 2" — because a subscription and a variable one-off could not share a
  // Stripe Checkout Session, so a user owing both paid TWICE, sequentially,
  // entering a card each time.
  //
  // Both legs now bill the same Stripe customer, so one card covers both: the
  // step hands the checkout every owed line at once and there is no "step 1 of
  // 2" any more. What must hold is that BOTH are asked for together.
  it("asks for both legs at once when both are owed", () => {
    renderStep(CLOUD_AND_RELIANT);

    expect(lastLine("compute")).toMatchObject({ covered: false });
    expect(lastLine("credit")).toMatchObject({ covered: false });
    expect(mockPanelRequests.at(-1)?.computePlanId).toBe("plan_compute_small");
    expect(mockPanelRequests.at(-1)?.creditCents).toBeGreaterThan(0);

    // The flow is no longer numbered, because there is no second payment to
    // number towards.
    expect(screen.queryByText(/step 1 of 2/i)).toBeNull();
    expect(screen.queryByText(/step 2 of 2/i)).toBeNull();
  });

  // A leg a coupon already covered stays ON the page, marked covered, rather
  // than vanishing — otherwise the code reads as though it did nothing at the
  // exact moment the user is being asked for money. It is simply not charged.
  it("shows a covered leg rather than hiding it", async () => {
    const { rerender } = renderStep(CLOUD_AND_RELIANT);
    expect(lastLine("compute")).toMatchObject({ covered: false });

    // The compute webhook landed (a coupon, or the subscription).
    currentFacts = { computeEligible: true, walletFunded: false };
    rerender(
      <CheckoutStep
        plan={CLOUD_AND_RELIANT as LaunchPlan}
        updatePlan={vi.fn()}
        onNext={vi.fn()}
        onBack={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(lastLine("compute")).toMatchObject({ covered: true });
    });
    // Still listed, and now not billed.
    expect(mockPanelRequests.at(-1)?.computePlanId).toBeUndefined();
    expect(lastLine("credit")).toMatchObject({ covered: false });
  });
});

describe("CheckoutStep — nothing fires from an effect", () => {
  // THE CLASS PROHIBITION. A call that creates, cancels or charges may issue
  // only from an explicit user action or a webhook — never from an effect
  // observing a state change. That rule is why the speculative CreateDaemon
  // was removed from ComputeStep, and a payment screen is the last place to
  // reintroduce it.
  it("does not commit merely by rendering", async () => {
    renderStep(CLOUD_OWN_KEY);
    await act(async () => {
      await Promise.resolve();
    });
    expect(mockRunCommit).not.toHaveBeenCalled();
  });

  // Even the state that LOOKS like success on its own must not act. Facts
  // flipping under the step is a webhook landing for some other reason — a
  // coupon redeemed in another tab, say — and the correct response is for
  // derivation to move the user on, not for this step to start provisioning.
  it("does not commit when the facts flip without a confirmation", async () => {
    const { rerender } = renderStep(CLOUD_OWN_KEY);
    currentFacts = { computeEligible: true, walletFunded: true };
    rerender(
      <CheckoutStep
        plan={CLOUD_OWN_KEY as LaunchPlan}
        updatePlan={vi.fn()}
        onNext={vi.fn()}
        onBack={vi.fn()}
      />,
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(mockRunCommit).not.toHaveBeenCalled();
  });
});

describe("CheckoutStep — after the server confirms", () => {
  it("commits once, and records the compute leg as settled", async () => {
    const updatePlan = vi.fn();
    const onNext = vi.fn();
    renderStep(CLOUD_OWN_KEY, { updatePlan, onNext });

    // The server agrees by the time the step re-reads.
    currentFacts = { computeEligible: true, walletFunded: true };
    await act(async () => {
      fireEvent.click(screen.getByTestId("confirm-payment"));
    });

    await waitFor(() => expect(mockRunCommit).toHaveBeenCalledTimes(1));
    expect(updatePlan).toHaveBeenCalledWith({ computeSettled: true });
    expect(onNext).toHaveBeenCalled();
  });

  // F2 AT THE WRITE SITE. Settlement names the leg it paid for, and a plan
  // that never owed a leg must not claim to have settled it — a cloud plan on
  // the user's own API key writes `computeSettled` and NOTHING about credit,
  // so a later switch to Reliant's models still has to pay for it.
  it("records only the legs this plan could owe", async () => {
    const updatePlan = vi.fn();
    renderStep(CLOUD_OWN_KEY, { updatePlan });

    currentFacts = { computeEligible: true, walletFunded: true };
    await act(async () => {
      fireEvent.click(screen.getByTestId("confirm-payment"));
    });

    await waitFor(() => expect(mockRunCommit).toHaveBeenCalled());
    const settlement = updatePlan.mock.calls
      .map(([u]) => u as Record<string, unknown>)
      .find((u) => "computeSettled" in u || "creditSettled" in u);
    expect(settlement).toEqual({ computeSettled: true });
  });

  it("records the credit leg for a plan that bought AI credit", async () => {
    const updatePlan = vi.fn();
    currentFacts = { computeEligible: true, walletFunded: false };
    renderStep(LOCAL_RELIANT, { updatePlan });

    currentFacts = { computeEligible: true, walletFunded: true };
    await act(async () => {
      fireEvent.click(screen.getByTestId("confirm-payment"));
    });

    await waitFor(() => expect(mockRunCommit).toHaveBeenCalled());
    expect(updatePlan).toHaveBeenCalledWith({ creditSettled: true });
  });

  // Both legs bought in one visit: both recorded, so neither can be re-charged
  // and neither silently covers a bill it did not pay.
  it("records both legs for a plan that bought both", async () => {
    const updatePlan = vi.fn();
    renderStep(CLOUD_AND_RELIANT, { updatePlan });

    currentFacts = { computeEligible: true, walletFunded: true };
    await act(async () => {
      fireEvent.click(screen.getByTestId("confirm-payment"));
    });

    await waitFor(() => expect(mockRunCommit).toHaveBeenCalled());
    expect(updatePlan).toHaveBeenCalledWith({
      computeSettled: true,
      creditSettled: true,
    });
  });

  it("commits under the plan's own key, so a later step does not commit again", async () => {
    currentFacts = { computeEligible: false, walletFunded: false };
    renderStep(CLOUD_OWN_KEY);
    currentFacts = { computeEligible: true, walletFunded: true };
    await act(async () => {
      fireEvent.click(screen.getByTestId("confirm-payment"));
    });

    await waitFor(() => expect(mockRunCommit).toHaveBeenCalled());
    // The key comes from the plan, not from component state — which is what
    // makes this commit and a terminal step's commit the SAME commit.
    expect(mockRunCommit.mock.calls[0][0]).toMatchObject({
      commitKey: "commit-key-1",
    });
  });

  // The rule that must not be relaxed: entitlement is webhook-driven, and the
  // browser observing a payment is not the server granting one. A `paid: true`
  // written against facts that never moved derives the user straight back to a
  // payment they already made.
  it("does not mark the plan paid while the server still says money is owed", async () => {
    vi.useFakeTimers();
    try {
      const updatePlan = vi.fn();
      renderStep(CLOUD_OWN_KEY, { updatePlan });

      // The webhook never lands.
      currentFacts = { computeEligible: false, walletFunded: false };
      fireEvent.click(screen.getByTestId("confirm-payment"));

      await act(async () => {
        await vi.advanceTimersByTimeAsync(70_000);
      });

      expect(updatePlan).not.toHaveBeenCalledWith(
        expect.objectContaining({ computeSettled: true }),
      );
      expect(mockRunCommit).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("says so honestly, and offers to check again, when confirmation times out", async () => {
    vi.useFakeTimers();
    try {
      renderStep(CLOUD_OWN_KEY);
      currentFacts = { computeEligible: false, walletFunded: false };
      fireEvent.click(screen.getByTestId("confirm-payment"));

      await act(async () => {
        await vi.advanceTimersByTimeAsync(70_000);
      });

      expect(screen.getByText(/haven't been able to confirm/i)).toBeVisible();
      expect(
        screen.getByRole("button", { name: /check again/i }),
      ).toBeEnabled();
      // And it never claims success.
      expect(screen.queryByText(/you're all set/i)).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("CheckoutStep — prices come from the server", () => {
  // Shipping this step on a client-side price table would put an invented
  // number next to a real card form.
  //
  // The tiles have moved OUT of this step entirely — the compute step shows
  // them, with prices, at the moment the machine is chosen. What this step
  // still does is PRICE the summary, and it must price it from the catalog.
  it("prices the summary from the catalog, not from a hardcoded number", () => {
    renderStep(CLOUD_OWN_KEY);
    expect(lastLine("compute")).toMatchObject({ amountCents: 2000 });
  });

  // A machine chosen on the compute step is the one billed here. Re-deriving a
  // plan ("the smallest we can find") would be a second opinion about what the
  // user is buying, and could silently switch it.
  it("bills the plan chosen earlier, and asks for none when there is no choice", () => {
    renderStep({ compute: "cloud_paid", modelProvider: "anthropic" });
    // No computePlanId on the plan: nothing to subscribe to, so nothing is
    // requested rather than a guess being substituted.
    expect(mockPanelRequests.at(-1)?.computePlanId).toBeUndefined();
  });

  // Deliberate: which payment methods are available depends on the browser and
  // on Dashboard configuration, so a static row promises methods we then fail
  // to show. Stripe's own UI enumerates what is actually offered.
  it("renders no payment-method logos of its own", () => {
    renderStep(CLOUD_OWN_KEY);
    for (const brand of [/apple pay/i, /google pay/i, /^link$/i, /^visa$/i]) {
      expect(screen.queryByText(brand)).toBeNull();
    }
  });
});
