/**
 * With "Use your own computer" chosen, the hosted machine rows fold into one
 * disclosure so the connect instructions are visible without scrolling. The
 * rows stay one click away, and picking one switches back to hosted.
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { LaunchPlan } from "../types";

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [],
    activeDaemon: undefined,
    loading: false,
    refresh: vi.fn(),
  }),
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
          priceCents: 2000n,
          structuredLimits: {
            allowedDaemonSizes: ["small"],
            daemonComputeIncludedMinutes: 1020,
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

function renderStep(updatePlan = vi.fn(async () => {})) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  render(
    <ComputeStep
      plan={{} as Partial<LaunchPlan>}
      updatePlan={updatePlan}
      onNext={vi.fn()}
      onBack={vi.fn()}
    />,
    {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    },
  );
  return updatePlan;
}

beforeEach(() => vi.clearAllMocks());

describe("ComputeStep — cloud machines fold away when Free is chosen", () => {
  it("shows hosted rows and no disclosure until Free is chosen", () => {
    renderStep();
    expect(screen.getByText("Small")).toBeVisible();
    expect(screen.queryByTestId("compute-cloud-toggle")).toBeNull();
  });

  it("collapses to a labelled disclosure, keeps connect instructions, and re-expands", () => {
    renderStep();
    fireEvent.click(screen.getByRole("button", { name: /Use your own computer/ }));

    const toggle = screen.getByTestId("compute-cloud-toggle");
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(toggle).toHaveTextContent("Reliant Cloud machines · from $20.00/mo");
    expect(screen.queryByText("Small")).toBeNull();
    expect(screen.getByTestId("self-hosted-connect")).toBeVisible();

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Small")).toBeVisible();
  });

  it("picking a hosted row switches back to hosted", () => {
    const updatePlan = renderStep();
    fireEvent.click(screen.getByRole("button", { name: /Use your own computer/ }));
    fireEvent.click(screen.getByTestId("compute-cloud-toggle"));
    fireEvent.click(screen.getByText("Small"));

    expect(updatePlan).toHaveBeenCalledWith({ computePlanId: "plan_compute_small" });
    expect(screen.queryByTestId("compute-cloud-toggle")).toBeNull();
    expect(screen.queryByTestId("self-hosted-connect")).toBeNull();
  });
});
