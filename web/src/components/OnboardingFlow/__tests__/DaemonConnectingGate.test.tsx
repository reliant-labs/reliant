/**
 * DaemonConnectingGate covers the four post-onboarding states:
 *   - connecting (polling, status is PENDING/DISCONNECTED, inside 60s window)
 *   - connected (status flips to ACTIVE)
 *   - failed (status is FAILED OR the 60s timeout elapses)
 *   - stuck disconnected (same UI as failed)
 *
 * We mock the api module to control what listDaemons returns per poll, and
 * exercise the pure `derivePhase` helper directly for boundary checks that
 * are awkward to drive through the React Query timer machinery.
 *
 * Test strategy: vitest fake timers + manual `act(advanceTimersByTimeAsync)`
 * to flush both the listDaemons promise (microtask) and the RQ
 * `refetchInterval` macrotask. We deliberately avoid RTL's `waitFor` because
 * it polls on real timers, which conflicts with the fake timer setup.
 */
import { act, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { Daemon } from "@/services/controlPlane/daemon";

// ── Mock the daemon service module ───────────────────────────

const mockListDaemons = vi.fn<() => Promise<{ daemons: Daemon[] }>>();

vi.mock("@/services/controlPlane/daemon", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/services/controlPlane/daemon")>();
  return {
    ...actual,
    listDaemons: (...args: Parameters<typeof mockListDaemons>) =>
      mockListDaemons(...args),
  };
});

function makeDaemon(partial: Partial<Daemon>): Daemon {
  return partial as unknown as Daemon;
}

// The failed state now navigates in-app (no external admin app), so stub
// useNavigate with a spy we can assert against. The gate isn't rendered under
// a RouterProvider in this test.
const mockNavigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
}));

// Imported AFTER mocks above so the gate picks them up.
import {
  DaemonConnectingGate,
  derivePhase,
  POLL_TIMEOUT_MS,
} from "../DaemonConnectingGate";

// ── Test harness ─────────────────────────────────────────────

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

/** Push the React/RQ event loop forward by `ms` and let queued microtasks
 *  (Promise.then callbacks from the mocked listDaemons) settle. */
async function flush(ms = 0) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

// Status constants (matching control-plane DaemonStatus enum).
const PENDING = 1;
const ACTIVE = 2;
const DISCONNECTED = 4;
const FAILED = 5;

beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
});

// ── Pure derivePhase ─────────────────────────────────────────

describe("derivePhase", () => {
  it("returns connecting when daemon is PENDING and within the window", () => {
    expect(derivePhase(makeDaemon({ status: PENDING }), 5_000)).toBe("connecting");
  });

  it("returns connecting when daemon is DISCONNECTED and within the window", () => {
    expect(derivePhase(makeDaemon({ status: DISCONNECTED }), 30_000)).toBe("connecting");
  });

  it("returns connecting when daemon is missing entirely (still provisioning)", () => {
    expect(derivePhase(undefined, 10_000)).toBe("connecting");
  });

  it("returns connected when daemon is ACTIVE", () => {
    expect(derivePhase(makeDaemon({ status: ACTIVE }), 1_000)).toBe("connected");
  });

  it("returns failed when daemon is FAILED, regardless of elapsed time", () => {
    expect(derivePhase(makeDaemon({ status: FAILED }), 100)).toBe("failed");
  });

  it("returns failed after the 60s window even if status is still PENDING", () => {
    expect(derivePhase(makeDaemon({ status: PENDING }), POLL_TIMEOUT_MS)).toBe("failed");
  });

  it("returns failed after timeout when daemon is DISCONNECTED (stuck case)", () => {
    expect(
      derivePhase(makeDaemon({ status: DISCONNECTED }), POLL_TIMEOUT_MS + 1),
    ).toBe("failed");
  });
});

// ── Component states ─────────────────────────────────────────

describe("DaemonConnectingGate", () => {
  it("renders the connecting state with elapsed seconds while status is PENDING", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [makeDaemon({ status: PENDING })] });

    const onContinue = vi.fn();
    render(<DaemonConnectingGate onContinue={onContinue} />, {
      wrapper: makeWrapper(),
    });

    // Flush the initial query → listDaemons promise resolution.
    await flush(50);

    expect(screen.getByTestId("daemon-gate-connecting")).toBeInTheDocument();
    expect(
      screen.getByText(/Starting your machine\.\.\./i),
    ).toBeInTheDocument();
    expect(screen.getByText(/Elapsed: 0s/)).toBeInTheDocument();
    expect(onContinue).not.toHaveBeenCalled();
  });

  it("flips to the connected state when the daemon goes ACTIVE", async () => {
    // First poll returns PENDING; subsequent polls return ACTIVE.
    mockListDaemons
      .mockResolvedValueOnce({ daemons: [makeDaemon({ status: PENDING })] })
      .mockResolvedValue({ daemons: [makeDaemon({ status: ACTIVE })] });

    const onContinue = vi.fn();
    render(<DaemonConnectingGate onContinue={onContinue} />, {
      wrapper: makeWrapper(),
    });

    // Initial fetch resolves to PENDING → connecting phase.
    await flush(50);
    expect(screen.getByTestId("daemon-gate-connecting")).toBeInTheDocument();

    // Advance past the 2s refetch interval. Polls every 2s; we step 2500ms to
    // be safely past the boundary, then flush microtasks so the new data is
    // committed.
    await flush(2_500);

    expect(screen.getByTestId("daemon-gate-connected")).toBeInTheDocument();
    expect(screen.getByText(/Connected/i)).toBeInTheDocument();

    // Clicking Continue invokes the parent callback (which navigates to /).
    act(() => {
      screen.getByRole("button", { name: /Continue/i }).click();
    });
    expect(onContinue).toHaveBeenCalledTimes(1);
  });

  it("renders the failed state when status is FAILED, surfacing last_status_message", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [
        makeDaemon({
          id: "daemon-abc-123",
          status: FAILED,
          name: "onboarding-daemon",
          lastStatusMessage: "Image pull failed: ECR rate limit",
        }),
      ],
    });

    const onContinue = vi.fn();
    render(<DaemonConnectingGate onContinue={onContinue} />, {
      wrapper: makeWrapper(),
    });
    await flush(50);

    expect(screen.getByTestId("daemon-gate-failed")).toBeInTheDocument();
    expect(screen.getByText(/Couldn't reach your machine/i)).toBeInTheDocument();
    expect(
      screen.getByText(/Image pull failed: ECR rate limit/),
    ).toBeInTheDocument();

    // "View machine" navigates in-app to the Machines settings
    // section, deep-linking to the failing machine via the `daemon`
    // search param (keyed by the machine's UUID).
    act(() => {
      screen.getByRole("button", { name: /View machine/i }).click();
    });
    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/settings/$section",
      params: { section: "environments" },
      search: { daemon: "daemon-abc-123" },
    });

    // Skip and continue is the escape hatch — exits without retrying.
    act(() => {
      screen.getByRole("button", { name: /Skip and continue/i }).click();
    });
    expect(onContinue).toHaveBeenCalledTimes(1);
  });

  it("falls back to a generic message when last_status_message is empty", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [makeDaemon({ status: FAILED, name: "daemons/x" })],
    });

    render(<DaemonConnectingGate onContinue={vi.fn()} />, {
      wrapper: makeWrapper(),
    });
    await flush(50);

    expect(screen.getByTestId("daemon-gate-failed")).toBeInTheDocument();
    expect(
      screen.getByText(
        /We couldn't reach your machine\. This is usually a network or configuration problem\./,
      ),
    ).toBeInTheDocument();
  });

  it("treats a daemon stuck in DISCONNECTED after the 60s window as failed", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [makeDaemon({ status: DISCONNECTED, name: "daemons/stuck" })],
    });

    render(<DaemonConnectingGate onContinue={vi.fn()} />, {
      wrapper: makeWrapper(),
    });

    // Initial fetch resolves into connecting state.
    await flush(50);
    expect(screen.getByTestId("daemon-gate-connecting")).toBeInTheDocument();

    // Burn through the 60s window. The elapsed-time ticker + RQ refetch
    // interval both run on timers, so advancing the clock flushes both.
    await flush(POLL_TIMEOUT_MS + 1_000);

    expect(screen.getByTestId("daemon-gate-failed")).toBeInTheDocument();
    // No status message → falls back to the generic copy.
    expect(
      screen.getByText(/We couldn't reach your machine/),
    ).toBeInTheDocument();
  });

  it("Retry re-polls listDaemons", async () => {
    // Both attempts return FAILED so we can re-enter the failed state after
    // Retry without having to coordinate a status flip.
    mockListDaemons.mockResolvedValue({
      daemons: [makeDaemon({ status: FAILED, name: "daemons/x" })],
    });

    render(<DaemonConnectingGate onContinue={vi.fn()} />, {
      wrapper: makeWrapper(),
    });
    await flush(50);

    expect(screen.getByTestId("daemon-gate-failed")).toBeInTheDocument();
    const callsBefore = mockListDaemons.mock.calls.length;

    act(() => {
      screen.getByRole("button", { name: /Retry/i }).click();
    });

    // Retry resets attemptStartedAt → new query key → fresh fetch fires.
    await flush(50);
    expect(mockListDaemons.mock.calls.length).toBeGreaterThan(callsBefore);
  });
});