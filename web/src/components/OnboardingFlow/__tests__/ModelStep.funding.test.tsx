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
};

const mockUseWalletOverview = vi.fn<() => WalletReturn>();

vi.mock("@/hooks/useReliantAIQueries", () => ({
  useWalletOverview: () => mockUseWalletOverview(),
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
  return render(
    <ModelStep
      plan={plan as LaunchPlan}
      updatePlan={overrides.updatePlan ?? vi.fn()}
      onNext={overrides.onNext ?? vi.fn()}
      onBack={vi.fn()}
    />,
    { wrapper },
  );
}

/** The Reliant CTA, under either label. */
function startButton() {
  return screen.getByRole("button", {
    name: /(start|continue) with reliant/i,
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

  // The coupon field is what remains, and it must not have been removed along
  // with the exit — a user holding a code can still fund the wallet here and
  // skip the checkout step entirely.
  it("still offers coupon redemption when unfunded", () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });
    renderStep();
    expect(screen.getByLabelText(/coupon code/i)).toBeVisible();
  });

  it("does not put a real coupon code in the input placeholder", () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });
    renderStep();
    const input = screen.getByLabelText(/coupon code/i);
    expect(input.getAttribute("placeholder")).not.toMatch(/launch/i);
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

    expect(updatePlan).toHaveBeenCalledWith({
      modelProvider: "reliant_credits",
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
      expect(updatePlan).toHaveBeenCalledWith({
        modelProvider: "reliant_credits",
      });
    });
  });
});
