/**
 * A coupon covers the coupon unit (M), not the cheapest machine.
 *
 * S1 and S2 sell below M, so "the first tile" is no longer the size a
 * coupon-only user may start: the server admits M alone
 * (`allowed_daemon_sizes`). Marking S1 "Covered" would promise a machine the
 * daemon service refuses at the last step of onboarding.
 */
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import type { LaunchPlan } from "../types";

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [] as DaemonInfo[],
    activeDaemon: undefined,
    loading: false,
    refresh: vi.fn(),
  }),
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({
    eligible: true,
    reason: null,
    isLoading: false,
    refetch: vi.fn(),
    allowedDaemonSizes: ["m"],
  }),
  computeIneligibleCopy: () => "Redeem a coupon code or choose a plan.",
}));

vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({
    isLoading: false,
    data: {
      plans: [
        {
          id: "plan_compute_s1",
          name: "S1",
          displayOrder: 1,
          priceCents: 2500n,
          structuredLimits: { allowedDaemonSizes: ["s1"], includedSmallDaemonHours: 160n },
        },
        {
          id: "plan_compute_m",
          name: "M",
          displayOrder: 2,
          priceCents: 3900n,
          structuredLimits: { allowedDaemonSizes: ["s1", "s2", "m"], includedSmallDaemonHours: 160n },
        },
      ],
    },
  }),
}));

vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));
vi.mock("@/components/Machines/PairMachinePanel", () => ({
  PairMachinePanel: () => <div data-testid="self-hosted-connect" />,
}));
vi.mock("@/hooks/useBundledDaemonPending", () => ({ useBundledDaemonPending: () => false }));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true, managedCredits: true, gitConnections: true },
}));
vi.mock("@/services/controlPlane/reliantAI", () => ({
  RedeemedCouponKind: { COMPUTE_MINUTES: 1, WALLET_CREDIT: 2 },
}));

import { ComputeStep } from "../steps/ComputeStep";

function renderStep() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return render(
    <ComputeStep plan={{} as Partial<LaunchPlan>} updatePlan={vi.fn(async () => {})} onNext={vi.fn()} onBack={vi.fn()} />,
    { wrapper },
  );
}

describe("ComputeStep — a coupon covers the coupon unit", () => {
  it("marks M covered and still prices the cheaper S1", () => {
    renderStep();

    expect(screen.getByTestId("compute-step-coverage-note")).toHaveTextContent(/covers the M machine/);
    // M carries no price; S1, which the coupon does not run, still does.
    expect(screen.queryByText(/\$39\.00\/mo/)).toBeNull();
    expect(screen.getByText(/\$25\.00\/mo/)).toBeInTheDocument();
  });
});
