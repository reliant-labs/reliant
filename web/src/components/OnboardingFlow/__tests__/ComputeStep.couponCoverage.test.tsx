/**
 * What the compute step shows once a coupon has covered the machine.
 *
 * ── The report ────────────────────────────────────────────────────────
 *
 * > "entering a coupon on that initial page, took me to this page... which
 * > seems wrong.... or maybe it just kind of 'hid the options'? and
 * > pre-selected the free option?"
 *
 * Nothing navigated. `showPlanChoice` was `!eligible`, and redeeming a compute
 * coupon flips `eligible` true — so the "Choose your machine" heading and all
 * four priced tiles were removed from the page on the render after Redeem
 * returned. The card visibly rearranged under the user at the moment they
 * acted, which is indistinguishable from having been moved to a different
 * screen.
 *
 * ── Why hiding was the wrong answer ───────────────────────────────────
 *
 * The reasoning behind it is sound as far as it goes: an entitled user has
 * nothing to buy, and a price list invites them to buy a second machine. But
 * "do not SELL to them" is not the same as "do not tell them what they got".
 * The sizes are still a real choice — a coupon grants minutes, not a machine
 * size — and removing them takes away the only place that choice is made,
 * silently, as a reward for redeeming.
 *
 * So the tiles stay, and what changes is the PRICES: they are marked as
 * covered rather than charged. That keeps the choice available, makes the
 * coupon's effect visible where the money used to be, and cannot be mistaken
 * for a navigation event because nothing leaves the page.
 */
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import type { LaunchPlan } from "../types";

const mockUseDaemonStatus = vi.fn(() => ({
  daemons: [] as DaemonInfo[],
  activeDaemon: undefined as DaemonInfo | undefined,
  loading: false,
  refresh: vi.fn(),
}));

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => mockUseDaemonStatus(),
}));

const mockUseCloudEligibility = vi.fn(() => ({
  eligible: false,
  reason: null as string | null,
  isLoading: false,
  grantedMinutesRemaining: 0,
  refetch: vi.fn(),
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => mockUseCloudEligibility(),
  computeIneligibleCopy: () => "Redeem a coupon code or choose a plan.",
}));

/** A priced, purchasable catalog — four sizes, exactly like production. */
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({
    isLoading: false,
    data: {
      plans: [
        {
          id: "plan_compute_small",
          name: "Small",
          displayOrder: 1,
          priceCents: 2000n,
          structuredLimits: {
            allowedDaemonSizes: ["small"],
            daemonComputeIncludedMinutes: 1020,
            daemonOveragePerMinuteCents: 2,
          },
        },
        {
          id: "plan_compute_medium",
          name: "Medium",
          displayOrder: 2,
          priceCents: 4000n,
          structuredLimits: {
            allowedDaemonSizes: ["small", "medium"],
            daemonComputeIncludedMinutes: 2520,
            daemonOveragePerMinuteCents: 2,
          },
        },
      ],
    },
  }),
}));

vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));

vi.mock("@/components/Projects/SelfHostedDaemonConnect", () => ({
  SelfHostedDaemonConnect: () => <div data-testid="self-hosted-connect" />,
}));

vi.mock("@/hooks/useBundledDaemonPending", () => ({
  useBundledDaemonPending: () => false,
}));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true, managedCredits: true, gitConnections: true },
}));

vi.mock("@/services/controlPlane/reliantAI", () => ({
  RedeemedCouponKind: { COMPUTE_MINUTES: 1, WALLET_CREDIT: 2 },
}));

import { ComputeStep } from "../steps/ComputeStep";

function wrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

function renderStep(updatePlan = vi.fn(async () => {})) {
  return render(
    <ComputeStep
      plan={{} as Partial<LaunchPlan>}
      updatePlan={updatePlan}
      onNext={vi.fn()}
      onBack={vi.fn()}
    />,
    { wrapper: wrapper() },
  );
}

beforeEach(() => {
  // clearAllMocks() drops mockReturnValue as well as call history, so every
  // mock whose RETURN matters is re-armed here. Without re-arming eligibility
  // the entitled value set by one test leaks into the next.
  vi.clearAllMocks();
  mockUseDaemonStatus.mockReturnValue({
    daemons: [],
    activeDaemon: undefined,
    loading: false,
    refresh: vi.fn(),
  });
  mockUseCloudEligibility.mockReturnValue({
    eligible: false,
    reason: null,
    isLoading: false,
    grantedMinutesRemaining: 0,
    refetch: vi.fn(),
  });
});

describe("ComputeStep — a coupon covered the machine", () => {
  function asEntitled() {
    mockUseCloudEligibility.mockReturnValue({
      eligible: true,
      reason: null,
      isLoading: false,
      grantedMinutesRemaining: 6000,
      refetch: vi.fn(),
    });
  }

  // THE BUG. The tiles vanished the instant the coupon was accepted.
  it("keeps the machine sizes on the page", () => {
    asEntitled();
    renderStep();

    expect(screen.getByText(/choose your machine/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /small/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /medium/i })).toBeInTheDocument();
  });

  // Coverage is per SIZE, not blanket, and this is the assertion that keeps
  // the page honest. The server resolves a coupon-only user's size allowance
  // from `plan_compute_free` — small alone — so marking Medium "Covered" would
  // promise a machine that `checkDaemonSizeAllowed` refuses at provisioning
  // with "your plan does not include daemon size medium". Observed in dev
  // against a real DEVTESTCOMPUTE redemption.
  it("marks only the size the grant actually pays for", () => {
    asEntitled();
    renderStep();

    // Small is covered: no price on it.
    expect(screen.queryByText(/\$20\.00\/mo/)).toBeNull();
    expect(screen.getAllByText(/covered/i).length).toBeGreaterThan(0);

    // Medium is NOT, and still shows what it costs — so choosing it reads as
    // the purchase it is.
    expect(screen.getByText(/\$40\.00\/mo/)).toBeInTheDocument();
  });

  // And it must say so in words, including the limit. "We start one for you"
  // is true but says nothing about the code just redeemed, and nothing about
  // why the bigger machines still carry a price.
  it("tells the user what their code covered, and what it does not", () => {
    asEntitled();
    renderStep();

    const note = screen.getByTestId("compute-step-coverage-note");
    expect(note).toHaveTextContent(/covers/i);
    expect(note).toHaveTextContent(/larger machines need a monthly plan/i);
  });

  // An entitled user still records a size, so the machine that gets
  // provisioned is the one they picked. Writing nothing here is what made the
  // commit fall back to SMALL regardless of the tile.
  it("records the chosen size for an entitled user", async () => {
    asEntitled();
    const updatePlan = vi.fn(async () => {});
    renderStep(updatePlan);

    screen.getByRole("button", { name: /use a reliant machine/i }).click();

    await vi.waitFor(() => {
      expect(updatePlan).toHaveBeenCalled();
    });
    const written = updatePlan.mock.calls[0][0] as Partial<LaunchPlan>;
    expect(written.compute).toBe("cloud_paid");
    expect(written.computePlanId).toBe("plan_compute_small");
  });

  // The un-entitled page is unchanged: real prices, and no coverage note.
  it("still prices the machines for a user with no coupon", () => {
    renderStep();

    expect(screen.getByText(/\$20\.00\/mo/)).toBeInTheDocument();
    expect(screen.queryByTestId("compute-step-coverage-note")).toBeNull();
  });
});

/**
 * The two options are ONE list with ONE price column.
 *
 * They used to be a two-column grid of cards, and it broke when the machine
 * tiles moved inline: the cloud card grew to ~550px while the local card held
 * ~106px of content, and because grid items stretch to the row height the
 * local card rendered 445px of empty black — measured in the browser at
 * 1200px, 81% dead space.
 *
 * These assertions pin the PROPERTY that fixed it rather than the classes that
 * implement it: the free option carries a price, on the same axis as the paid
 * ones, so the two are comparable. A future change that restores a side-by-side
 * layout is fine as long as that stays true.
 */
describe("ComputeStep — the free option is a peer, not an afterthought", () => {
  it("gives the local option a price, so it reads against the paid ones", () => {
    renderStep();

    const local = screen.getByRole("button", { name: /use your own computer/i });
    // "Free" is INSIDE the option, not a detached badge elsewhere on the page:
    // that is what puts it in the same column as "$20.00/mo".
    expect(local).toHaveTextContent(/free/i);
  });

  it("marks the local option as selectable state, not a bare link", () => {
    renderStep();

    const local = screen.getByRole("button", { name: /use your own computer/i });
    // Peers in a choice list announce their selected state; the paid tiles
    // already do (aria-pressed), and the free one must not be the odd one out.
    expect(local).toHaveAttribute("aria-pressed", "false");
  });

  it("separates the hosted options from the run-it-yourself one", () => {
    renderStep();
    expect(screen.getByText(/or run it yourself/i)).toBeInTheDocument();
  });

  // The local option is not a plan you buy, so it must never write a
  // computePlanId — that field drives the checkout summary and the provisioned
  // machine size.
  it("does not record a compute plan when the local option is picked", async () => {
    const updatePlan = vi.fn(async () => {});
    renderStep(updatePlan);

    screen.getByRole("button", { name: /use your own computer/i }).click();

    // Picking it only opens the connect instructions; it commits nothing.
    expect(updatePlan).not.toHaveBeenCalled();
  });
});
