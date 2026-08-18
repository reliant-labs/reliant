/**
 * ModelStep funding-gate tests.
 *
 * "Start with Reliant" must not be reachable without funding. Managed Reliant
 * draws on the org wallet, and the LLM proxy rejects a zero balance outright —
 * so finishing onboarding on an empty wallet strands the user at their FIRST
 * message, past the point where onboarding could have helped them.
 *
 * The gate is on funds, not eligibility: eligibility only says the account
 * COULD use managed Reliant. We mock the wallet + eligibility hooks because the
 * contract under test is exactly their return shapes, not the transport.
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

function startButton() {
  return screen.getByRole("button", { name: /start with reliant/i });
}

// ── Tests ────────────────────────────────────────────────────────────────

describe("ModelStep funding gate", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("blocks Start with Reliant on a zero balance", () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });
    renderStep();
    expect(startButton()).toBeDisabled();
  });

  it("blocks Start with Reliant when no wallet exists yet", () => {
    mockUseWalletOverview.mockReturnValue({ data: undefined, isLoading: false });
    renderStep();
    expect(startButton()).toBeDisabled();
  });

  it("blocks while the balance is still loading, rather than flashing enabled", () => {
    mockUseWalletOverview.mockReturnValue({ data: undefined, isLoading: true });
    renderStep();
    expect(startButton()).toBeDisabled();
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

  it("offers a billing route when unfunded", () => {
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });
    renderStep();
    expect(screen.getByRole("button", { name: /set up billing/i })).toBeVisible();
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

  // THE USER'S REPORT: "I was able to select reliant as an ai provider with no
  // coupon specified, and no billing setup."
  //
  // `disabled` is a RENDERING concern. A stale render, a keyboard activation
  // racing the wallet refetch, or devtools all reach the handler anyway, and
  // committing the choice sends the user into the app with a provider that
  // fails on their FIRST message (the LLM proxy rejects a zero balance).
  //
  // jsdom/React will not dispatch a click on a button React still considers
  // disabled, so the DOM cannot express "the handler ran anyway" — asserting
  // through it passes even with the guard deleted, which makes it a test of
  // nothing. Instead drive the SAME path the button's onClick drives, with the
  // attribute out of the picture entirely: render with the escape hatch that
  // enables the button, then flip the wallet to empty so the click lands while
  // `creditsAvailable` is false. That is precisely the stale-render race.
  it("does not commit Reliant when the handler runs on an empty wallet", async () => {
    // ?onboarding-credits=eligible forces the button enabled...
    const search = new URL(window.location.href);
    search.searchParams.set("onboarding-credits", "eligible");
    window.history.replaceState({}, "", search.toString());

    // ...while the wallet is genuinely empty.
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });

    const updatePlan = vi.fn();
    const onNext = vi.fn();
    renderStep({ updatePlan, onNext });

    const btn = startButton();
    expect(btn).toBeEnabled(); // the forced-eligible path
    await act(async () => {
      fireEvent.click(btn);
    });

    // The forced flag is a DEV escape hatch for exercising the UI; it must not
    // let a real unfunded account commit. Balance is the only real authority.
    expect(updatePlan).not.toHaveBeenCalled();
    expect(onNext).not.toHaveBeenCalled();

    window.history.replaceState({}, "", "/");
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
