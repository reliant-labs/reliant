/**
 * A coupon that clears the bill must advance the user.
 *
 * ── The bug ───────────────────────────────────────────────────────────
 *
 * `onRedeemed={() => void facts.refetch()}` — a refetch and nothing else.
 * Meanwhile the Stripe path calls `settleAndAdvance()`, which polls until the
 * server agrees the debt is cleared, writes the per-leg settlement flag, fires
 * the commit, and advances. A coupon clearing the ONLY outstanding leg
 * therefore left the user parked on the checkout step reading a success
 * message, with nothing to press.
 *
 * Both are the same event — "the server granted entitlement, money question
 * settled" — so both go through the same path. Redemption is not a second way
 * to finish paying; it is the same finish.
 *
 * ── What must NOT change ──────────────────────────────────────────────
 *
 * A redemption is a user action, so routing it to the settle path does not
 * violate the no-effects rule. But the settlement flags it writes must still
 * be earned: `settleAndAdvance` re-reads the facts from the SERVER and writes
 * `computeSettled` / `creditSettled` only once they say the debt is gone. A
 * flag written from the redeem response alone would suppress the checkout
 * requirement for money that never moved — the F2 defect `requiresPayment.ts`
 * documents at length. The last test here pins exactly that.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LaunchPlan } from "../types";
import type { PaymentFacts } from "../requiresPayment";

// ── Mocks ────────────────────────────────────────────────────────────────

/**
 * The compute checkout, stubbed.
 *
 * It renders the REAL `RedeemCouponForm` (itself stubbed below) because the
 * coupon field now lives inside this component, beside the payment form,
 * rather than under it in the step. The step still owns what a redemption
 * MEANS — settle, commit, advance — which is what this file tests, so the stub
 * has to keep the coupon reachable or the subject disappears with the
 * component.
 */
vi.mock("@/components/Billing/ComputeSubscriptionCheckout", async () => {
  const { RedeemCouponForm } = await import("@/components/RedeemCouponForm");
  return {
    ComputeSubscriptionCheckout: ({
      onDone,
      confirmSettlement,
    }: {
      onDone: () => void;
      confirmSettlement: () => Promise<boolean>;
    }) => (
      <>
        <button type="button" data-testid="confirm-payment" onClick={onDone}>
          confirm
        </button>
        {/* Mirrors the real component: a redemption is gated on the SERVER
            agreeing the entitlement landed before anything is reported done. */}
        <RedeemCouponForm
          onRedeemed={() => {
            void confirmSettlement().then((ok) => {
              if (ok) onDone();
            });
          }}
        />
      </>
    ),
  };
});

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

vi.mock("../commitLaunchPlan", () => ({
  ensureCommitKey: vi.fn(async () => "commit-key-1"),
}));

/** Billing availability is a deployment constant; the tests vary entitlement. */
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

vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({
    data: {
      plans: [
        {
          id: "plan_compute_small",
          priceCents: 2000n,
          displayOrder: 1,
          structuredLimits: {
            allowedDaemonSizes: ["small"],
            daemonComputeIncludedMinutes: 960,
            daemonOveragePerMinuteCents: 2,
          },
        },
      ],
    },
    isLoading: false,
  }),
  useCreateComputeSubscriptionIntent: () => ({ mutateAsync: vi.fn() }),
  useCreateWalletTopupPaymentIntent: () => ({ mutateAsync: vi.fn() }),
  // A coupon path involves no card, so there is nothing to quote a fee on —
  // which is the point these tests already make about coupons.
  useWalletTopupQuote: () => ({ data: undefined, isLoading: false }),
  isCheckoutIdentityRequired: () => false,
}));

/**
 * The coupon form, stubbed down to the one thing the step depends on: a
 * successful redemption calls `onRedeemed`. The real form's own behaviour
 * (trim, validate, per-case server message) has its own tests.
 */
vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: ({ onRedeemed }: { onRedeemed?: (r: unknown) => void }) => (
    <button
      type="button"
      data-testid="redeem-coupon"
      onClick={() => onRedeemed?.({ kind: 1, computeMinutes: 6000 })}
    >
      redeem
    </button>
  ),
}));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

import { CheckoutStep } from "../steps/CheckoutStep";

// ── Harness ──────────────────────────────────────────────────────────────

const CLOUD_OWN_KEY: Partial<LaunchPlan> = {
  compute: "cloud_paid",
  modelProvider: "anthropic",
  computePlanId: "plan_compute_small",
};

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

beforeEach(() => {
  vi.clearAllMocks();
  currentFacts = { computeEligible: false, walletFunded: false };
});

// ── Tests ────────────────────────────────────────────────────────────────

describe("CheckoutStep — a coupon settles like a payment", () => {
  // THE BUG, stated as a test. A compute coupon clears the only outstanding
  // leg; the user must end up committed and advanced, not parked.
  it("commits and advances when a coupon clears the only outstanding leg", async () => {
    const updatePlan = vi.fn();
    const onNext = vi.fn();
    renderStep(CLOUD_OWN_KEY, { updatePlan, onNext });

    // The grant lands: the server now says compute is entitled.
    currentFacts = { computeEligible: true, walletFunded: false };
    await act(async () => {
      fireEvent.click(screen.getByTestId("redeem-coupon"));
    });

    await waitFor(() => expect(mockRunCommit).toHaveBeenCalledTimes(1));
    expect(updatePlan).toHaveBeenCalledWith({ computeSettled: true });
    expect(onNext).toHaveBeenCalled();
  });

  // Same settle path as Stripe means the same per-leg discipline: a coupon
  // that cleared compute must not also claim the credit leg.
  it("records only the leg the coupon actually cleared", async () => {
    const updatePlan = vi.fn();
    renderStep(CLOUD_OWN_KEY, { updatePlan });

    currentFacts = { computeEligible: true, walletFunded: false };
    await act(async () => {
      fireEvent.click(screen.getByTestId("redeem-coupon"));
    });

    await waitFor(() => expect(mockRunCommit).toHaveBeenCalled());
    const settlement = updatePlan.mock.calls
      .map(([u]) => u as Record<string, unknown>)
      .find((u) => "computeSettled" in u || "creditSettled" in u);
    expect(settlement).toEqual({ computeSettled: true });
  });

  // THE RULE THE OWNER NAMED. A redeem call returning ok is not entitlement;
  // the server granting it is. If the facts do not move, nothing is settled
  // and nothing is committed — otherwise a settlement flag would suppress the
  // checkout requirement for money that never moved.
  it("settles nothing when the server still says the debt stands", async () => {
    vi.useFakeTimers();
    try {
      const updatePlan = vi.fn();
      const onNext = vi.fn();
      renderStep(CLOUD_OWN_KEY, { updatePlan, onNext });

      // Redemption "succeeded" client-side, but no grant ever appears.
      currentFacts = { computeEligible: false, walletFunded: false };
      fireEvent.click(screen.getByTestId("redeem-coupon"));

      await act(async () => {
        await vi.advanceTimersByTimeAsync(70_000);
      });

      expect(updatePlan).not.toHaveBeenCalledWith(
        expect.objectContaining({ computeSettled: true }),
      );
      expect(mockRunCommit).not.toHaveBeenCalled();
      expect(onNext).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  // Redemption is a USER ACTION, so it may act. Rendering the form is not.
  it("does not commit merely by rendering the coupon form", async () => {
    renderStep(CLOUD_OWN_KEY);
    await act(async () => {
      await Promise.resolve();
    });
    expect(mockRunCommit).not.toHaveBeenCalled();
  });
});
