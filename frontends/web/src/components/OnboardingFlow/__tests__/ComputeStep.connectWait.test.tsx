/**
 * The compute step's waits on a machine are no longer silent.
 *
 *  - Desktop: the app's own machine has not registered yet (the "Getting your
 *    machine ready…" state).
 *  - Own computer: "Use your own computer" is chosen and the machine has not
 *    connected ("Waiting for your computer to connect…").
 *
 * Both show a counter from the first second and, past ~30s, a retry that asks
 * the registry again plus a way forward. One user waited 4m36s on a bare
 * spinner before giving up.
 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { LaunchPlan } from "../types";
import { CONNECT_WAIT_SLOW_MS } from "../ConnectWait";

const mocks = vi.hoisted(() => ({
  refresh: vi.fn(),
  bundledPending: false,
  loading: false,
}));

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [],
    activeDaemon: undefined,
    loading: mocks.loading,
    unreachable: false,
    refresh: mocks.refresh,
  }),
}));
vi.mock("@/hooks/useBundledDaemonPending", () => ({
  useBundledDaemonPending: () => mocks.bundledPending,
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({
    eligible: false,
    reason: null,
    isLoading: false,
    grantedSmallDaemonSecondsRemaining: 0,
    refetch: vi.fn(),
  }),
  computeIneligibleCopy: () => "",
}));
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({ isLoading: false, data: { plans: [] } }),
}));
vi.mock("@/components/RedeemCouponForm", () => ({ RedeemCouponForm: () => null }));
vi.mock("@/components/Machines/PairMachinePanel", () => ({
  PairMachinePanel: () => <div data-testid="pair-panel" />,
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

function renderStep(plan: Partial<LaunchPlan> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <StepFooterProvider>
      <ComputeStep plan={plan} updatePlan={vi.fn()} onNext={vi.fn()} onBack={vi.fn()} />
      <StepFooterOutlet />
    </StepFooterProvider>,
    { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> },
  );
}

const tick = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  mocks.bundledPending = false;
  mocks.loading = false;
});
afterEach(() => vi.useRealTimers());

describe("ComputeStep — waiting for the desktop app's machine", () => {
  beforeEach(() => {
    mocks.bundledPending = true;
  });

  it("shows progress, not a bare spinner", async () => {
    renderStep();
    expect(screen.getByTestId("compute-step-loading")).toBeInTheDocument();
    await tick(4_000);
    expect(screen.getByTestId("connect-wait-elapsed")).toHaveTextContent("4s");
  });

  it("after ~30s offers retry, which asks the registry again", async () => {
    renderStep();
    await tick(CONNECT_WAIT_SLOW_MS);

    fireEvent.click(screen.getByRole("button", { name: /retry/i }));
    expect(mocks.refresh).toHaveBeenCalledTimes(1);
  });

  it("'Choose a machine myself' gets the user out of the wait and onto the choice", async () => {
    renderStep();
    await tick(CONNECT_WAIT_SLOW_MS);

    fireEvent.click(screen.getByRole("button", { name: /choose a machine myself/i }));

    expect(screen.queryByTestId("compute-step-loading")).toBeNull();
    expect(screen.getByRole("button", { name: /use your own computer/i })).toBeInTheDocument();
  });
});

describe("ComputeStep — 'your own computer' chosen, machine not connected yet", () => {
  it("shows a counter next to the pairing panel and, past ~30s, a retry", async () => {
    renderStep({ compute: "local_daemon" });
    expect(screen.getByTestId("pair-panel")).toBeInTheDocument();
    expect(screen.getByText(/looking for your computer/i)).toBeInTheDocument();
    expect(screen.queryByTestId("connect-wait-slow")).toBeNull();

    await tick(CONNECT_WAIT_SLOW_MS);
    fireEvent.click(screen.getByRole("button", { name: /retry/i }));
    expect(mocks.refresh).toHaveBeenCalledTimes(1);
  });
});
