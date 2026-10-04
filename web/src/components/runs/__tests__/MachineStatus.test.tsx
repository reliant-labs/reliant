// Copyright (c) 2025 Reliant Labs

/**
 * MachineStatus (WORKFLOW_UI.md §9.2): one copy per daemon state, "Wake it"
 * only where waking helps, the automation note on unattended runs, and the
 * shared quota copy when a wake is refused. RunMachineBanner shows it only
 * while the run is waiting for its machine.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ChatActivity } from "@/gen/reliant/v1/chat_pb";
import type { Chat } from "@/api/client";

const mocks = vi.hoisted(() => ({
  daemons: [] as Array<{ daemonId: string; hostname: string; status: number; daemonType: string }>,
  daemonsLoading: false,
  resumeDaemon: vi.fn(),
  goToBilling: vi.fn(),
  triggers: [] as Array<{ id: string; daemonId: string }>,
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: mocks.daemonsLoading ? undefined : mocks.daemons, isLoading: mocks.daemonsLoading }),
  useResumeDaemon: (callbacks: { onError?: (error: unknown) => void } = {}) => ({
    mutate: (id: string) => {
      Promise.resolve(mocks.resumeDaemon(id)).catch((error) => callbacks.onError?.(error));
    },
    isPending: false,
    variables: undefined,
  }),
}));
vi.mock("@/hooks/useGoToBilling", () => ({ useGoToBilling: () => mocks.goToBilling }));
vi.mock("@/hooks/trigger-queries", () => ({
  useTriggers: () => ({ data: mocks.triggers }),
  useSetTriggerEnabled: () => ({ mutate: vi.fn(), isPending: false }),
}));

import { MachineStatus, RunMachineBanner } from "../MachineStatus";
import { useActivityStore } from "@/store/activityStore";

function daemon(status: DaemonStatus, overrides: Partial<(typeof mocks.daemons)[number]> = {}) {
  return { daemonId: "d-1", hostname: "MacBook (cloud)", status, daemonType: "managed", ...overrides };
}

beforeEach(() => {
  mocks.daemons = [];
  mocks.daemonsLoading = false;
  mocks.resumeDaemon.mockReset().mockResolvedValue({});
  mocks.goToBilling.mockReset();
  mocks.triggers = [];
  useActivityStore.setState({ entries: new Map(), activities: new Map(), maxSeenSeq: 0 });
});

describe("MachineStatus copy by daemon state", () => {
  it("suspended: asleep, waiting, and Wake it resumes that daemon", async () => {
    mocks.daemons = [daemon(DaemonStatus.SUSPENDED)];
    render(<MachineStatus daemonId="d-1" />);
    expect(screen.getByText("MacBook (cloud)")).toBeInTheDocument();
    expect(screen.getByText(/is asleep\. This run is waiting for it\./)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Wake it" }));
    expect(mocks.resumeDaemon).toHaveBeenCalledWith("d-1");
  });

  it("suspended, automation run: adds that schedules wake it", () => {
    mocks.daemons = [daemon(DaemonStatus.SUSPENDED)];
    render(<MachineStatus daemonId="d-1" automation />);
    expect(
      screen.getByText("Schedules wake it when they start; it went to sleep during this run."),
    ).toBeInTheDocument();
  });

  it("starting: starting, a pending dot, and no action", () => {
    mocks.daemons = [daemon(DaemonStatus.PENDING)];
    render(<MachineStatus daemonId="d-1" />);
    expect(screen.getByText(/is starting…/)).toBeInTheDocument();
    expect(screen.getByTestId("machine-status-starting-dot")).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("offline (local daemon): open Reliant on that machine, no action", () => {
    mocks.daemons = [daemon(DaemonStatus.DISCONNECTED, { hostname: "Sean's laptop", daemonType: "self_hosted" })];
    render(<MachineStatus daemonId="d-1" />);
    expect(screen.getByText("Sean's laptop")).toBeInTheDocument();
    expect(screen.getByText(/is offline\. Open Reliant on that machine to continue\./)).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("deleted: the machine was removed", () => {
    mocks.daemons = [daemon(DaemonStatus.ACTIVE, { daemonId: "d-other" })];
    render(<MachineStatus daemonId="d-1" />);
    expect(screen.getByText("The machine this run used was removed.")).toBeInTheDocument();
  });

  it("quota exhausted: a refused wake shows the shared copy and Billing", async () => {
    mocks.daemons = [daemon(DaemonStatus.SUSPENDED)];
    mocks.resumeDaemon.mockRejectedValue(new Error("[resource_exhausted] compute limit"));
    render(<MachineStatus daemonId="d-1" />);
    await userEvent.click(screen.getByRole("button", { name: "Wake it" }));
    expect(await screen.findByText(/used the compute included with your account/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Billing" }));
    expect(mocks.goToBilling).toHaveBeenCalled();
  });

  it("renders as a warning-accented banner, not a toast", () => {
    mocks.daemons = [daemon(DaemonStatus.SUSPENDED)];
    render(<MachineStatus daemonId="d-1" />);
    const banner = screen.getByTestId("machine-status");
    expect(banner).toHaveAttribute("role", "status");
    expect(banner.className).toContain("border-l-warning");
    expect(banner.className).toContain("bg-card");
  });
});

describe("RunMachineBanner", () => {
  const chat = (overrides: Partial<Chat> = {}) =>
    ({
      id: "chat-1",
      activity: ChatActivity.RUNNING,
      activeDaemonId: "d-1",
      launchKind: "chat.start",
      ...overrides,
    }) as Chat;

  it("is absent while the run is not waiting for its machine", () => {
    mocks.daemons = [daemon(DaemonStatus.SUSPENDED)];
    const { container } = render(<RunMachineBanner chat={chat()} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("appears when the run is WAITING_FOR_DAEMON, and follows live activity", async () => {
    mocks.daemons = [daemon(DaemonStatus.SUSPENDED)];
    render(<RunMachineBanner chat={chat({ activity: ChatActivity.WAITING_FOR_DAEMON })} />);
    expect(screen.getByTestId("machine-status")).toBeInTheDocument();

    useActivityStore.getState().applyStreamActivity("chat-1", ChatActivity.RUNNING, 5);
    await waitFor(() => expect(screen.queryByTestId("machine-status")).toBeNull());
  });

  it("falls back to the automation's machine when the chat has none, and notes the schedule", () => {
    mocks.daemons = [daemon(DaemonStatus.SUSPENDED, { daemonId: "d-trg" })];
    mocks.triggers = [{ id: "trg-1", daemonId: "d-trg" }];
    render(
      <RunMachineBanner
        chat={chat({
          activity: ChatActivity.WAITING_FOR_DAEMON,
          activeDaemonId: undefined,
          launchKind: "schedule",
          triggerId: "trg-1",
        })}
      />,
    );
    expect(screen.getByText(/is asleep/)).toBeInTheDocument();
    expect(screen.getByText(/Schedules wake it/)).toBeInTheDocument();
  });
});
