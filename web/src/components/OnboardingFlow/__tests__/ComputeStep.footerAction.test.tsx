/**
 * The compute step's way forward lives in the onboarding card's FOOTER, not at
 * the end of the step's scrolling content.
 *
 * It used to be the last thing in the step: a button under every machine row
 * and the notes beneath them. With six hosted sizes that pushed it below the
 * fold of a 1400x900 window, so a user who picked "Large" saw nothing happen
 * and no way on — the footer bar sat empty under the fold line the whole time.
 *
 * These tests render the step inside the real OnboardingPage footer slot and
 * assert the action is rendered INTO the footer, and that it names the
 * machine the user picked.
 */
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useState, type ReactNode } from "react";

import type { LaunchPlan } from "../types";

const mockDaemonStatus = vi.fn(() => ({
  daemons: [] as unknown[],
  activeDaemon: undefined as unknown,
  loading: false,
  refresh: vi.fn(),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => mockDaemonStatus(),
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({
    eligible: false,
    reason: null,
    isLoading: false,
    grantedMinutesRemaining: 0,
    refetch: vi.fn(),
  }),
  computeIneligibleCopy: () => "",
}));
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({
    isLoading: false,
    data: {
      plans: [
        {
          id: "plan_compute_small",
          name: "Small",
          displayOrder: 1,
          priceCents: 1500n,
          structuredLimits: { allowedDaemonSizes: ["small"] },
        },
        {
          id: "plan_compute_large",
          name: "Large",
          displayOrder: 3,
          priceCents: 7900n,
          structuredLimits: { allowedDaemonSizes: ["large"] },
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
import { StepFooterOutlet, StepFooterProvider } from "../StepFooter";

function renderInCard(initialPlan: Partial<LaunchPlan> = {}) {
  const updatePlan = vi.fn(async (_updates: Partial<LaunchPlan>) => {});
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  // Stateful like the real page: updatePlan writes the plan and the step
  // re-renders from it, so picking a row changes what the footer names.
  function Harness() {
    const [plan, setPlan] = useState<Partial<LaunchPlan>>(initialPlan);
    return (
      <ComputeStep
        plan={plan}
        updatePlan={async (updates) => {
          await updatePlan(updates);
          setPlan((current) => ({ ...current, ...updates }));
        }}
        onNext={vi.fn()}
        onBack={vi.fn()}
      />
    );
  }
  // The same shape OnboardingPage renders: the step in a scrolling body, and
  // the footer slot in a separate, fixed footer.
  render(
    <StepFooterProvider>
      <main data-testid="card-body">
        <Harness />
      </main>
      <footer data-testid="card-footer">
        <StepFooterOutlet />
      </footer>
    </StepFooterProvider>,
    {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    },
  );
  return updatePlan;
}

beforeEach(() => {
  vi.clearAllMocks();
  mockDaemonStatus.mockReturnValue({
    daemons: [],
    activeDaemon: undefined,
    loading: false,
    refresh: vi.fn(),
  });
});

describe("ComputeStep — the way forward is in the card footer", () => {
  it("renders the hosted-machine action in the footer, not the scrolling body", () => {
    renderInCard();
    const footer = screen.getByTestId("card-footer");
    expect(within(footer).getByRole("button", { name: /^Continue with / })).toBeVisible();
    expect(
      within(screen.getByTestId("card-body")).queryByRole("button", { name: /^Continue with / }),
    ).toBeNull();
  });

  it("names the machine the user picked", async () => {
    renderInCard();
    const footer = screen.getByTestId("card-footer");
    // Nothing picked yet: the first (smallest) size is the default.
    expect(within(footer).getByRole("button", { name: "Continue with Small" })).toBeVisible();

    await act(async () => {
      fireEvent.click(screen.getByText("Large"));
    });
    expect(within(footer).getByRole("button", { name: "Continue with Large" })).toBeVisible();
  });

  it("records the picked machine when the footer action is clicked", async () => {
    const updatePlan = renderInCard({ computePlanId: "plan_compute_large" });
    await act(async () => {
      fireEvent.click(
        within(screen.getByTestId("card-footer")).getByRole("button", {
          name: "Continue with Large",
        }),
      );
    });
    expect(updatePlan).toHaveBeenCalledWith(
      expect.objectContaining({ compute: "cloud_paid", computePlanId: "plan_compute_large" }),
    );
  });

  it("with Free chosen and nothing connected, the footer says it is waiting", () => {
    renderInCard();
    fireEvent.click(screen.getByRole("button", { name: /Use your own computer/ }));
    const footer = screen.getByTestId("card-footer");
    expect(within(footer).queryByRole("button", { name: /^Continue with / })).toBeNull();
    expect(within(footer).getByRole("button", { name: /Waiting for your computer/ })).toBeDisabled();
  });
});

describe("Plan rows — cost breakdown only on the selected row", () => {
  it("shows size facts for the selected row and hides them for the rest", async () => {
    // pricing facts come from daemonPricing, which the mock above omits, so
    // this asserts the prop wiring through PlanTileRow directly.
    const { PlanTileRow } = await import("@/components/Billing/PlanTiles");
    const facts = {
      burnRateLabel: "uses included hours 4× as fast",
      hourlyPriceLabel: "$0.52/h past your included hours",
      diskLabel: "100 GiB disk",
      suspendedFeeLabel: "$25.00/mo",
    };
    const option = {
      planId: "p",
      label: "Large",
      detail: "8 GB RAM · 2 CPU",
      monthlyPriceCents: 7900,
      facts,
    } as never;

    const { rerender } = render(
      <PlanTileRow plan={option} selected={false} showFacts="selected" onSelect={vi.fn()} />,
    );
    expect(screen.queryByTestId("plan-row-size-facts")).toBeNull();
    expect(screen.getByText("8 GB RAM · 2 CPU")).toBeVisible();

    rerender(<PlanTileRow plan={option} selected showFacts="selected" onSelect={vi.fn()} />);
    expect(screen.getByTestId("plan-row-size-facts")).toHaveTextContent("100 GiB disk");

    // Default stays "always" for the billing page's plan grid.
    rerender(<PlanTileRow plan={option} selected={false} onSelect={vi.fn()} />);
    expect(screen.getByTestId("plan-row-size-facts")).toBeVisible();
  });
});
