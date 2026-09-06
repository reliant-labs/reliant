/**
 * The consolidated checkout step.
 *
 * ── What is actually under test ───────────────────────────────────────
 *
 * Not Stripe. `EmbeddedCheckoutPanel` is stubbed, because it owns session
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

/** What `onDone` means: the server confirmed it. Nothing else calls it. */
const mockPanelRequests: unknown[] = [];
vi.mock("@/components/Billing/EmbeddedCheckoutPanel", () => ({
  EmbeddedCheckoutPanel: ({
    request,
    onDone,
  }: {
    request: unknown;
    onDone: () => void;
  }) => {
    mockPanelRequests.push(request);
    return (
      <button type="button" data-testid="confirm-payment" onClick={onDone}>
        confirm
      </button>
    );
  },
}));

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
let currentFacts: PaymentFacts = {
  computeEligible: false,
  walletFunded: false,
};
const mockFactsRefetch = vi.fn(async () => currentFacts);
vi.mock("../useOnboardingFacts", () => ({
  useOnboardingFacts: () => ({
    ...currentFacts,
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
    expect(mockPanelRequests).toContainEqual({
      kind: "compute_plan",
      planId: "plan_compute_small",
    });
  });

  it("buys wallet credit when only credit is owed", () => {
    currentFacts = { computeEligible: true, walletFunded: false };
    renderStep(LOCAL_RELIANT);
    expect(mockPanelRequests[0]).toMatchObject({ kind: "wallet_topup" });
  });

  // The trade-off, made visible. A subscription and a variable one-off cannot
  // share one Stripe Checkout Session while keeping both webhook paths intact,
  // so a user owing both pays twice — sequentially, in this step. What they
  // must never do is leave the flow between the two.
  it("starts with compute when both are owed, and labels it as 1 of 2", () => {
    renderStep(CLOUD_AND_RELIANT);
    expect(mockPanelRequests[0]).toMatchObject({ kind: "compute_plan" });
    expect(screen.getByText(/step 1 of 2/i)).toBeInTheDocument();
  });

  it("moves to the credit leg once compute is entitled, without navigating", async () => {
    const { rerender } = renderStep(CLOUD_AND_RELIANT);
    expect(mockPanelRequests[0]).toMatchObject({ kind: "compute_plan" });

    // The compute webhook landed.
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
      expect(mockPanelRequests.at(-1)).toMatchObject({ kind: "wallet_topup" });
    });
    expect(screen.getByText(/step 2 of 2/i)).toBeInTheDocument();
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
  it("commits once, and marks the plan paid", async () => {
    const updatePlan = vi.fn();
    const onNext = vi.fn();
    renderStep(CLOUD_OWN_KEY, { updatePlan, onNext });

    // The server agrees by the time the step re-reads.
    currentFacts = { computeEligible: true, walletFunded: true };
    await act(async () => {
      fireEvent.click(screen.getByTestId("confirm-payment"));
    });

    await waitFor(() => expect(mockRunCommit).toHaveBeenCalledTimes(1));
    expect(updatePlan).toHaveBeenCalledWith({ paid: true });
    expect(onNext).toHaveBeenCalled();
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

      expect(updatePlan).not.toHaveBeenCalledWith({ paid: true });
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
  // number next to a real card form. The plan grid renders the catalog's
  // price_cents and nothing else.
  it("renders the catalog's price, not a hardcoded one", () => {
    renderStep({ compute: "cloud_paid", modelProvider: "anthropic" });
    expect(screen.getByText(/\$20\.00\/mo/)).toBeInTheDocument();
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
