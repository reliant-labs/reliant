/**
 * ComputeStep render-time tests.
 *
 * Focus: the loading gate that blocks the radio + Continue button until the
 * first `listDaemons` query has settled. Before this gate existed, a fast
 * user could click "I'll connect my own" before the auto-skip useEffect
 * ever read `hasUsableDaemonForOnboarding(daemons)`, leaving them stranded
 * on the local-daemon path even though a cloud daemon was already wired up.
 *
 * We mock `useDaemonStatus` so we can deterministically drive its loading
 * shape — the gate is keyed on `loading` (TanStack's `isLoading`, true ONLY
 * during the initial fetch). Mocking the hook is preferred over mocking the
 * underlying gRPC client because the contract under test IS the hook return
 * shape, not the network layer.
 */
import { act, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import {
  DaemonStatus,
  type DaemonInfo,
} from "@/gen/reliant/v1/daemon_registry_pb";
import type { LaunchPlan } from "../types";

// ── Mocks ────────────────────────────────────────────────────────────────

type DaemonStatusReturn = {
  daemons: DaemonInfo[];
  activeDaemon: DaemonInfo | undefined;
  loading: boolean;
  refresh: () => void;
};

const mockUseDaemonStatus = vi.fn<() => DaemonStatusReturn>();

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => mockUseDaemonStatus(),
}));

// Stub the onboarding query hooks. The loading gate sits ahead of any of
// these in the render path, but they still mount for the non-loading
// branches of the test, so they need stable, network-free returns.
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({
    eligible: true,
    reason: null,
    isLoading: false,
  }),
  useCreateDaemon: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
  useResumeDaemon: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
  isReasonedQuotaError: () => false,
}));

// trackEvent fires on the auto-skip path; stub so the test doesn't depend
// on the analytics module being initialized.
vi.mock("@/lib/analytics", () => ({
  trackEvent: vi.fn(),
}));

// Capabilities flag controls whether the cloud option renders; force it on
// so the form's "Reliant Cloud" CTA is unambiguously present when we expect
// the form to be visible.
vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true, managedCredits: true, gitConnections: true },
}));

// EventBus context needs a provider; the simplest path is to mock the hook
// to return a no-op bus so we don't pull in the real provider's setup.
vi.mock("@/lib/event-context", () => ({
  useEventBus: () => ({
    emit: vi.fn(),
    on: vi.fn(() => () => {}),
  }),
}));

// jsdom doesn't ship a ResizeObserver, and DaemonConnectionDiagrams (which
// the form branch renders) constructs one to fluidly scale its SVG. A
// minimal no-op stub is enough — the tests don't assert any scaling
// behavior, they only need the component tree to mount without throwing.
class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
// @ts-expect-error: minimal stub for jsdom.
globalThis.ResizeObserver = ResizeObserverStub;

// Imported AFTER mocks above so ComputeStep picks them up.
import { ComputeStep } from "../steps/ComputeStep";

// ── Test harness ─────────────────────────────────────────────────────────

function makeWrapper() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
    },
  });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

function makeDaemon(partial: Partial<DaemonInfo>): DaemonInfo {
  return partial as unknown as DaemonInfo;
}

function renderComputeStep(opts: {
  daemons: DaemonInfo[];
  loading: boolean;
  onNext?: () => void;
  updatePlan?: (updates: Partial<LaunchPlan>) => Promise<void> | void;
}) {
  const activeDaemon = opts.daemons.find(
    (d) => d.status === DaemonStatus.ACTIVE,
  );
  mockUseDaemonStatus.mockReturnValue({
    daemons: opts.daemons,
    activeDaemon,
    loading: opts.loading,
    refresh: vi.fn(),
  });

  return render(
    <ComputeStep
      plan={{}}
      updatePlan={opts.updatePlan ?? vi.fn(async () => {})}
      onNext={opts.onNext ?? vi.fn()}
      onBack={vi.fn()}
    />,
    { wrapper: makeWrapper() },
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  vi.restoreAllMocks();
});

// ── Tests ────────────────────────────────────────────────────────────────

describe("ComputeStep loading gate", () => {
  it("hides the form behind the loading state while daemonLoading is true", () => {
    renderComputeStep({ daemons: [], loading: true });

    // The deterministic loading marker is present.
    expect(screen.getByTestId("compute-step-loading")).toBeInTheDocument();
    expect(screen.getByText(/Checking your workspace/i)).toBeInTheDocument();

    // The form's CTAs are NOT in the DOM — there is no path through which
    // a click can race the auto-skip evaluation.
    expect(
      screen.queryByRole("button", { name: /Start cloud daemon/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /I'll connect my own/i }),
    ).not.toBeInTheDocument();
  });

  it("renders the form once daemonLoading is false and no usable daemon is registered", () => {
    renderComputeStep({ daemons: [], loading: false });

    // Loading marker is gone, form is back.
    expect(screen.queryByTestId("compute-step-loading")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Start cloud daemon/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /I'll connect my own/i }),
    ).toBeInTheDocument();
  });

  it("auto-skips when daemonLoading is false and a usable (ACTIVE) daemon is present", async () => {
    const onNext = vi.fn();
    const updatePlan = vi.fn(async () => {});

    await act(async () => {
      renderComputeStep({
        daemons: [makeDaemon({ id: "d-1", status: DaemonStatus.ACTIVE })],
        loading: false,
        onNext,
        updatePlan,
      });
    });

    // The auto-skip effect commits the local-daemon plan and advances.
    expect(updatePlan).toHaveBeenCalledWith(
      expect.objectContaining({
        compute: "local_daemon",
        daemonLocation: "self_hosted",
      }),
    );
    expect(onNext).toHaveBeenCalledTimes(1);
  });

  it("auto-skips for IDLE daemons too (transitional cloud / local reconnect state)", async () => {
    const onNext = vi.fn();
    const updatePlan = vi.fn(async () => {});

    await act(async () => {
      renderComputeStep({
        daemons: [makeDaemon({ id: "d-idle", status: DaemonStatus.IDLE })],
        loading: false,
        onNext,
        updatePlan,
      });
    });

    expect(onNext).toHaveBeenCalledTimes(1);
    expect(updatePlan).toHaveBeenCalledWith(
      expect.objectContaining({ compute: "local_daemon" }),
    );
  });
});
