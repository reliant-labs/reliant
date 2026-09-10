/**
 * ModelStep funding tests — the INVERTED gate.
 *
 * ── What changed, and why the old assertions had to go ────────────────
 *
 * This file used to assert that "Start with Reliant" is `disabled` on an empty
 * wallet and that `finishOnboarding` refuses the choice. That was correct while
 * the only remedy on offer was a link out to `/settings/billing`: committing
 * `reliant_credits` unfunded sent the user into the app to fail at their first
 * message (the LLM proxy rejects a zero balance outright), so blocking beat
 * stranding.
 *
 * A checkout step now exists INSIDE the flow, and that inverts the argument.
 * Blocking here is the stranding: the user is refused at the one screen that
 * could take their money, on the way to the one screen that could fix it. So
 * the step records the choice, and `deriveStep` routes an unfunded
 * `reliant_credits` plan to `checkout`.
 *
 * The guarantee is NOT weakened, it MOVED — from a disabled button to step
 * derivation, where it is enumerated over the whole state space in
 * `deriveStep.enumeration.test.ts`. This file pins the half that lives here:
 * the choice is recorded, and no billing exit remains.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { LaunchPlan } from "../types";

// ── Mocks ────────────────────────────────────────────────────────────────

type WalletReturn = {
  data: { wallet?: { balanceUsdNanos?: bigint } } | undefined;
  isLoading: boolean;
  // The step refetches after a coupon redemption — the wallet balance is the
  // same server fact `requiresPayment` reads, so redeeming settles the credit
  // leg only once the SERVER says so.
  refetch?: () => Promise<unknown>;
};

const mockUseWalletOverview = vi.fn<() => WalletReturn>();

vi.mock("@/hooks/useReliantAIQueries", () => ({
  useWalletOverview: () => ({
    refetch: vi.fn().mockResolvedValue({}),
    ...mockUseWalletOverview(),
  }),
  useRedeemCoupon: () => ({ mutate: vi.fn(), isPending: false }),
}));

// Compute eligibility is mocked INELIGIBLE on purpose, and the module is
// mocked at all only so that a REGRESSION would be caught rather than
// silently passing: `useCloudEligibility` answers "may this account run a
// cloud daemon" — a compute question — so the model step must not consult it.
// If someone re-imports it here, these funded-wallet cases go red.
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({
    eligible: false,
    reason: "Compute is not available yet",
    isLoading: false,
  }),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
}));

// OAuth hooks reach for browser/IPC surfaces that jsdom doesn't provide; the
// Reliant (builtIn) branch under test never invokes them.
vi.mock("@/hooks", () => ({
  useCodexOAuth: () => ({ reset: vi.fn(), start: vi.fn() }),
  useClaudeOAuth: () => ({ reset: vi.fn(), start: vi.fn() }),
  useCopilotOAuth: () => ({ reset: vi.fn(), start: vi.fn() }),
  useOAuthAvailability: () => ({ available: true, isLoading: false }),
}));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

vi.mock("@/api/client", () => ({
  api: { settings: { updateProvider: vi.fn(), validateProvider: vi.fn() } },
}));

vi.mock("@/lib/events", () => ({
  getEventBus: () => ({ emit: vi.fn(), on: vi.fn(() => () => {}) }),
}));

vi.mock("@/store/apiKeySetupStore", () => ({
  useApiKeySetupStore: () => ({ open: vi.fn(), close: vi.fn() }),
}));

// Dev must NOT be a funding shortcut — see the eligibility comment in
// ModelStep. Pinning it false keeps this test honest about that. Partial mock:
// lib/constants also exports `isDev`, which logger.ts reads at import time.
vi.mock("@/lib/constants", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/constants")>()),
  getIsDev: () => false,
}));

// Imported AFTER mocks so ModelStep picks them up.
import { ModelStep } from "../steps/ModelStep";

// ── Harness ──────────────────────────────────────────────────────────────

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

const plan: Partial<LaunchPlan> = { compute: "cloud_free_trial" };

function renderStep(
  overrides: { updatePlan?: () => void; onNext?: () => void } = {},
) {
  const result = render(
    <ModelStep
      plan={plan as LaunchPlan}
      updatePlan={overrides.updatePlan ?? vi.fn()}
      onNext={overrides.onNext ?? vi.fn()}
      onBack={vi.fn()}
    />,
    { wrapper },
  );
  // The step opens with Claude Code pre-selected — the free path, so a user
  // can finish onboarding without being charged. Every test in this file is
  // about the FUNDING GATE on the Reliant CTA, which is not on screen until
  // Reliant is chosen, so the choice is made here rather than each test
  // silently depending on which provider happens to be the default.
  fireEvent.click(screen.getByRole("button", { name: /^reliant$/i }));
  return result;
}

/** The Reliant CTA, under any of its labels. Reliant is selected by renderStep.
 *
 *  Unfunded, the label now names the amount being bought ("Continue with
 *  $25.00 credit") rather than saying "Continue with Reliant" — the amount is
 *  chosen on THIS step, so the button states what pressing it commits to. */
function startButton() {
  return screen.getByRole("button", {
    name: /(start with reliant|continue with \$)/i,
  });
}

// ── Tests ────────────────────────────────────────────────────────────────

describe("ModelStep funding gate", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  // THE INVERSION. Each of these three used to assert `toBeDisabled()`.
  it("lets the user proceed on a zero balance — checkout catches them", () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });
    renderStep();
    expect(startButton()).toBeEnabled();
  });

  it("lets the user proceed when no wallet exists yet", () => {
    mockUseWalletOverview.mockReturnValue({ data: undefined, isLoading: false });
    renderStep();
    expect(startButton()).toBeEnabled();
  });

  it("does not flicker disabled while the balance is still loading", () => {
    mockUseWalletOverview.mockReturnValue({ data: undefined, isLoading: true });
    renderStep();
    expect(startButton()).toBeEnabled();
  });

  it("allows Start with Reliant once the wallet is funded", () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 2_000_000_000n } },
      isLoading: false,
    });
    renderStep();
    expect(startButton()).toBeEnabled();
  });

  it("allows Start with Reliant on a funded wallet with no compute entitlement", () => {
    // The mocked useCloudEligibility above reports ineligible. Compute is a
    // different product bought separately; a user who redeemed LLM credit but
    // no compute minutes must still be able to spend that credit.
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 2_000_000_000n } },
      isLoading: false,
    });
    renderStep();
    expect(startButton()).toBeEnabled();
  });

  it("does not blame compute availability when the wallet is unfunded", () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });
    renderStep();
    expect(screen.queryByText(/compute is not available/i)).toBeNull();
  });

  // The owner's ask, at this step: onboarding must not leave the flow. The
  // "Set up billing" link navigated to /settings/billing, needing a `returnTo`
  // round-trip to get the user back into a wizard whose entire state is a URL
  // search param.
  it("offers no route out to the billing settings page when unfunded", () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });
    renderStep();
    expect(
      screen.queryByRole("button", { name: /set up billing/i }),
    ).toBeNull();
  });

  // ── INVERTED, and this is the owner's stated engagement priority ─────
  //
  // This used to assert the OPPOSITE: no coupon field here, because
  // "everything that moves money lives in ONE place — the checkout step".
  // That reasoning is right about a CARD and wrong about a COUPON. A coupon is
  // how a leg is cleared WITHOUT paying, so putting the only coupon field on
  // the screen that exists to collect a card forced a user holding codes for
  // both legs to walk onto a payment form to redeem the second one.
  //
  // The requirement is that such a user never sees a card at all. Redeeming
  // here is what makes it true: both legs settle while the user is still
  // deciding, `requiresPayment` returns nothing owed, and `deriveStep` never
  // routes to checkout.
  //
  // The drift the old comment feared is avoided by the SAME mechanism the
  // checkout step uses — redemption refetches the wallet, and the wallet
  // balance is the fact derivation reads. Nothing here writes a settlement
  // flag, so this step cannot claim an entitlement the server has not granted.
  it("offers a coupon field when unfunded, so both-coupon users never see a card", () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });
    renderStep();
    expect(
      screen.getByRole("button", { name: /have a coupon/i }),
    ).toBeInTheDocument();
  });

  // The mirror: a funded wallet has nothing left to buy, so neither the amount
  // nor the coupon is offered. Showing them would invite a purchase the user
  // does not need.
  it("offers no amount or coupon once the wallet is funded", () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 2_000_000_000n } },
      isLoading: false,
    });
    renderStep();
    expect(screen.queryByRole("button", { name: /have a coupon/i })).toBeNull();
    expect(screen.queryByText(/how much credit/i)).toBeNull();
  });

  // The amount is chosen HERE, beside the provider, and carried on the plan so
  // the single checkout at the end bills what was decided rather than re-asking.
  it("records the chosen credit amount on the plan", async () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });
    const updatePlan = vi.fn();
    renderStep({ updatePlan });

    // $50.00 — a preset other than the default, so the assertion cannot pass
    // by accident.
    fireEvent.click(screen.getByRole("button", { name: "$50.00" }));

    expect(updatePlan).toHaveBeenCalledWith({ aiCreditCents: 5000 });
  });

  // THE ORIGINAL USER REPORT: "I was able to select reliant as an ai provider
  // with no coupon specified, and no billing setup."
  //
  // Still a real complaint, still answered — but no longer here. Selecting
  // Reliant unfunded is now ALLOWED, because what comes next is the checkout
  // step rather than the app. The guarantee that such a user cannot reach a
  // first message unfunded moved into `deriveStep`, where it is enumerated over
  // the whole state space rather than resting on one button's `disabled`
  // attribute.
  //
  // What must hold HERE is that the click actually records the choice, because
  // that is what derivation routes on. A step that swallowed the click would
  // leave the user pressing a button that does nothing — a worse failure than
  // the `disabled` it replaced, since at least that one admitted to itself.
  it("records the Reliant choice on an empty wallet so checkout can pick it up", async () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });

    const updatePlan = vi.fn();
    const onNext = vi.fn();
    renderStep({ updatePlan, onNext });

    await act(async () => {
      fireEvent.click(startButton());
    });

    // The amount chosen on this step rides along, so the single checkout at
    // the end bills what was decided here rather than asking a second time.
    expect(updatePlan).toHaveBeenCalledWith({
      modelProvider: "reliant_credits",
      aiCreditCents: 2500,
    });
    expect(onNext).toHaveBeenCalled();
  });

  // A funded wallet must still be able to proceed, or the guard is just a wall.
  it("commits Reliant when the wallet is funded", async () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 2_000_000_000n } },
      isLoading: false,
    });
    const updatePlan = vi.fn();
    const onNext = vi.fn();
    renderStep({ updatePlan, onNext });

    fireEvent.click(startButton());

    await waitFor(() => {
      // A funded wallet buys nothing, so no amount is recorded — writing one
      // would describe a purchase that is not happening.
      expect(updatePlan).toHaveBeenCalledWith({
        modelProvider: "reliant_credits",
        aiCreditCents: undefined,
      });
    });
  });
});
