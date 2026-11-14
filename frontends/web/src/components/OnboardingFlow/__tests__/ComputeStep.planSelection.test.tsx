/**
 * The machine picker selects ONE machine.
 *
 * The report (owner, 2026-11-06): clicking S1 or S2 selected both, and both
 * showed $21.48/mo. Pricing v2 holds S1 off sale, so `plan_compute_s2`
 * (allowing s1 and s2) was the cheapest plan for both sizes; the rows shared a
 * `planId`, and selection is by `planId`. Choosing "S1" also bought and
 * provisioned the S2 machine. These pin one row per plan, named by the size
 * the plan provisions, and a single pressed row.
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
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
  grantedSmallDaemonSecondsRemaining: 0,
  refetch: vi.fn(),
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => mockUseCloudEligibility(),
  computeIneligibleCopy: () => "Redeem a coupon code or choose a plan.",
}));

// Today's catalog shape (control-plane internal/plansconfig/pricing.yaml,
// pricing v2): no S1 plan; S2 at $21.48 runs s1 and s2; M at $32.36 runs
// s1, s2 and m.
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({
    isLoading: false,
    data: {
      plans: [
        {
          id: "plan_compute_s2",
          name: "Compute S2",
          displayOrder: 1,
          priceCents: 2148n,
          structuredLimits: { allowedDaemonSizes: ["s1", "s2"], machineIncludedHours: 160n },
        },
        {
          id: "plan_compute_m",
          name: "Compute M",
          displayOrder: 2,
          priceCents: 3236n,
          structuredLimits: { allowedDaemonSizes: ["s1", "s2", "m"], machineIncludedHours: 160n },
        },
      ],
      daemonPricing: { sizes: [], storageCentsPerGibMonth: "25", placeholder: false },
    },
  }),
}));

vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));

vi.mock("@/components/Machines/PairMachinePanel", () => ({
  PairMachinePanel: () => <div data-testid="self-hosted-connect" />,
}));

vi.mock("@/hooks/useBundledDaemonPending", () => ({
  useBundledDaemonPending: () => false,
}));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: {
    cloudDaemons: true,
    managedCredits: true,
    gitConnections: true,
  },
}));

vi.mock("@/services/controlPlane/reliantAI", () => ({
  RedeemedCouponKind: { COMPUTE_MINUTES: 1, WALLET_CREDIT: 2 },
}));

import { ComputeStep } from "../steps/ComputeStep";

function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

function renderStep(plan: Partial<LaunchPlan>, updatePlan = vi.fn(async () => {})) {
  render(
    <ComputeStep plan={plan} updatePlan={updatePlan} onNext={vi.fn()} onBack={vi.fn()} />,
    { wrapper: wrapper() },
  );
  return updatePlan;
}

const machineRows = () => screen.getAllByRole("button", { name: /^(S1|S2|M|L|XL) / });

beforeEach(() => {
  vi.clearAllMocks();
});

describe("ComputeStep machine picker selection", () => {
  it("shows one row per plan: no S1 row that would buy the S2 plan", () => {
    renderStep({});
    expect(machineRows()).toHaveLength(2);
    expect(screen.getByRole("button", { name: /^S2 / })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^M / })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^S1 / })).not.toBeInTheDocument();
    expect(screen.getAllByText("$21.48/mo")).toHaveLength(1);
  });

  it("clicking S2 selects S2 alone and records its plan", () => {
    const updatePlan = renderStep({});
    fireEvent.click(screen.getByRole("button", { name: /^S2 / }));
    expect(updatePlan).toHaveBeenCalledWith({ computePlanId: "plan_compute_s2" });
  });

  it.each([
    ["plan_compute_s2", "S2"],
    ["plan_compute_m", "M"],
  ])("with %s chosen, exactly one row is pressed (%s)", (computePlanId, label) => {
    renderStep({ computePlanId });
    const pressed = machineRows().filter((row) => row.getAttribute("aria-pressed") === "true");
    expect(pressed).toHaveLength(1);
    expect(pressed[0]).toHaveAccessibleName(new RegExp(`^${label} `));
  });
});
