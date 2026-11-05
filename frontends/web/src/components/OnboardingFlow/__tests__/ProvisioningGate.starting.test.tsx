/**
 * "Starting your machine…" must not wear a check mark.
 *
 * The commit reports `starting` once a machine has been REQUESTED. The row
 * beneath it is a spinner until the daemon gate sees the machine ACTIVE, so the
 * checklist and the live status below it never contradict each other. These
 * tests drive the real DaemonConnectingGate against a stubbed daemon registry.
 */
import { act, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { DaemonInfo as Daemon } from "@/gen/reliant/v1/daemon_registry_pb";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { setTransport } from "@/lib/connect";
import { daemonRegistryTransport } from "@/test/daemonRegistry";

const mockListDaemons = vi.fn<() => Promise<{ daemons: Daemon[] }>>();

beforeEach(() => {
  mockListDaemons.mockReset();
  vi.useFakeTimers();
  setTransport(daemonRegistryTransport({ listDaemons: () => mockListDaemons() }));
});
afterEach(() => {
  // Timers first: a pending poll must still find the stubbed transport.
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
  setTransport(null);
});

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));

import { ProvisioningGate } from "../ProvisioningGate";
import { STARTING_MACHINE_DETAIL, type CommitResult } from "../commitLaunchPlan";

const daemon = (status: DaemonStatus) => ({ daemonId: "d-1", status }) as unknown as Daemon;

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function startingCommit(): CommitResult {
  return {
    commitKey: "k",
    status: "complete",
    daemonId: "d-1",
    tasks: [
      { name: "grant_ai_access", status: "complete", detail: "AI access ready." },
      { name: "provision_daemon", status: "starting", detail: STARTING_MACHINE_DETAIL, daemonId: "d-1" },
    ],
  };
}

async function flush(ms = 0) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

const machineRow = () => screen.getByTestId("task-provision_daemon");

describe("ProvisioningGate — a requested machine is not a running machine", () => {
  it("shows a spinner, not a check, while the machine is still PENDING", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    render(<ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} />, { wrapper });
    await flush(50);

    const row = machineRow();
    expect(row).toHaveAttribute("data-status", "starting");
    expect(row.querySelector('[data-testid="task-spinner"]')).not.toBeNull();
    expect(row.querySelector('[data-testid="task-done-icon"]')).toBeNull();
    // The other task finished and keeps its check.
    expect(
      screen.getByTestId("task-grant_ai_access").querySelector('[data-testid="task-done-icon"]'),
    ).not.toBeNull();
  });

  it("names the stages of the wait", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    render(<ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} />, { wrapper });
    await flush(50);

    expect(machineRow()).toHaveTextContent(/requesting compute.*pulling the image.*starting reliant.*connected/i);
  });

  it("says what skipping does", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    render(<ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} />, { wrapper });
    await flush(50);

    expect(screen.getByRole("button", { name: /Skip and continue/i })).toBeInTheDocument();
    expect(screen.getByText(/keeps starting in the background/i)).toBeInTheDocument();
  });

  it("turns the row into a check only once the machine is ACTIVE", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    render(<ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} />, { wrapper });
    await flush(50);
    expect(machineRow().querySelector('[data-testid="task-done-icon"]')).toBeNull();

    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.ACTIVE)] });
    await flush(2_500);

    expect(machineRow()).toHaveAttribute("data-status", "complete");
    expect(machineRow().querySelector('[data-testid="task-done-icon"]')).not.toBeNull();
    expect(screen.getByText("Your machine is ready")).toBeInTheDocument();
  });

  it("shows a failure, not a check, when the machine FAILED", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.FAILED)] });
    render(<ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} />, { wrapper });
    await flush(50);

    expect(machineRow()).toHaveAttribute("data-status", "failed");
    expect(machineRow().querySelector('[data-testid="task-done-icon"]')).toBeNull();
  });

  it("never draws a check for an ACTIVE-less machine at any point before ACTIVE", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [] });
    render(<ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} />, { wrapper });
    // Before the first poll lands, and after several polls with no machine yet.
    expect(machineRow().querySelector('[data-testid="task-done-icon"]')).toBeNull();
    for (let i = 0; i < 5; i++) {
      await flush(2_000);
      expect(machineRow().querySelector('[data-testid="task-done-icon"]')).toBeNull();
    }
  });
});

describe("ProvisioningGate — a machine that failed to start can be started again", () => {
  const startAgain = () => screen.queryByRole("button", { name: /Start your machine again/i });

  it("offers no re-provision while the machine is still starting", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    render(
      <ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} onRetry={vi.fn()} />,
      { wrapper },
    );
    await flush(50);
    expect(startAgain()).toBeNull();
  });

  it("re-runs the commit when the machine FAILED", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.FAILED)] });
    const onRetry = vi.fn();
    render(
      <ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} onRetry={onRetry} />,
      { wrapper },
    );
    await flush(50);

    await act(async () => {
      startAgain()!.click();
    });
    expect(onRetry).toHaveBeenCalledTimes(1);
    // One click, one re-run: the button holds until the new commit lands.
    expect(startAgain()).toBeNull();
    expect(screen.getByRole("button", { name: /Starting your machine again/i })).toBeDisabled();
  });

  it("re-runs the commit when the machine never came up in time", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    const onRetry = vi.fn();
    render(
      <ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} onRetry={onRetry} />,
      { wrapper },
    );
    await flush(300_000 + 2_000);

    expect(machineRow()).toHaveAttribute("data-status", "failed");
    await act(async () => {
      startAgain()!.click();
    });
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("treats the retried commit as a fresh attempt", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.FAILED)] });
    const { rerender } = render(
      <ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} onRetry={vi.fn()} />,
      { wrapper },
    );
    await flush(50);
    expect(machineRow()).toHaveAttribute("data-status", "failed");

    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    rerender(
      <ProvisioningGate commit={startingCommit()} onContinue={vi.fn()} onRetry={vi.fn()} />,
    );
    await flush(50);

    expect(machineRow()).toHaveAttribute("data-status", "starting");
    expect(startAgain()).toBeNull();
  });
});

describe("ProvisioningGate — local compute never sees cloud-provisioning copy", () => {
  it("renders no machine row and no connecting gate for a local-daemon plan", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    render(
      <ProvisioningGate commit={startingCommit()} compute="local_daemon" onContinue={vi.fn()} />,
      { wrapper },
    );
    await flush(50);

    expect(screen.queryByTestId("task-provision_daemon")).not.toBeInTheDocument();
    expect(screen.queryByTestId("daemon-gate-connecting")).not.toBeInTheDocument();
    expect(screen.queryByText(/requesting compute/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/starting your machine/i)).not.toBeInTheDocument();
    expect(mockListDaemons).not.toHaveBeenCalled();
  });

  it.each(["cloud_free_trial", "cloud_paid"] as const)(
    "still narrates the machine for %s",
    async (compute) => {
      mockListDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
      render(
        <ProvisioningGate commit={startingCommit()} compute={compute} onContinue={vi.fn()} />,
        { wrapper },
      );
      await flush(50);

      expect(screen.getByTestId("task-provision_daemon")).toBeInTheDocument();
      expect(screen.getByTestId("daemon-gate-connecting")).toBeInTheDocument();
    },
  );
});
