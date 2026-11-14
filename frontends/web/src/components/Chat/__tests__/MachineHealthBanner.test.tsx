// Copyright (c) 2026 Reliant Labs

/**
 * The chat's machine-health banner says, with the daemon's reason, that the
 * chat's machine is connected but cannot start processes, and stays out of the
 * way otherwise. Regression for 2026-10-30 (reliant#1919): every command on a
 * machine failed for 24 minutes while the app showed it connected and healthy.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import { DaemonInfoSchema, DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { MachineHealthSchema, MachineHealthState } from "@/gen/reliant/v1/tools_daemon_pb";

const state = vi.hoisted(() => ({ daemons: [] as unknown[], activeId: "" }));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: state.daemons }),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ activeDaemon: state.activeId ? { daemonId: state.activeId } : undefined }),
}));
import { MachineHealthBanner } from "../MachineHealthBanner";

const REASON = "This machine cannot start new processes: its process table is full.";

function machine(over: { daemonId?: string; state?: MachineHealthState; sinceMs?: bigint; status?: DaemonStatus } = {}): DaemonInfo {
  return create(DaemonInfoSchema, {
    daemonId: over.daemonId ?? "d-1",
    name: "default",
    status: over.status ?? DaemonStatus.ACTIVE,
    health: create(MachineHealthSchema, {
      state: over.state ?? MachineHealthState.DEGRADED,
      code: "process_table_full",
      reason: REASON,
      sinceMs: over.sinceMs ?? 1_000n,
    }),
  });
}

beforeEach(() => {
  sessionStorage.clear();
  state.daemons = [];
  state.activeId = "";
});
afterEach(cleanup);

describe("MachineHealthBanner", () => {
  it("says the chat's machine cannot start processes, and why", () => {
    state.daemons = [machine()];
    render(<MachineHealthBanner daemonId="d-1" />);
    const banner = screen.getByTestId("machine-health-banner");
    expect(banner).toHaveAttribute("role", "alert");
    expect(within(banner).getByText("This machine can't start new processes")).toBeInTheDocument();
    expect(within(banner).getByText(REASON)).toBeInTheDocument();
  });

  it("falls back to the app's machine when the chat names none", () => {
    state.daemons = [machine()];
    state.activeId = "d-1";
    render(<MachineHealthBanner />);
    expect(screen.getByTestId("machine-health-banner")).toBeInTheDocument();
  });

  it("says nothing for a healthy machine, another machine, or one that is not connected", () => {
    state.daemons = [
      machine({ daemonId: "ok", state: MachineHealthState.OK }),
      machine({ daemonId: "other" }),
      machine({ daemonId: "off", status: DaemonStatus.DISCONNECTED }),
    ];
    const { rerender } = render(<MachineHealthBanner daemonId="ok" />);
    expect(screen.queryByTestId("machine-health-banner")).toBeNull();
    rerender(<MachineHealthBanner daemonId="off" />);
    expect(screen.queryByTestId("machine-health-banner")).toBeNull();
    rerender(<MachineHealthBanner daemonId="missing" />);
    expect(screen.queryByTestId("machine-health-banner")).toBeNull();
  });

  it("stays dismissed for this episode, and comes back for the next", async () => {
    state.daemons = [machine({ sinceMs: 1_000n })];
    const { rerender } = render(<MachineHealthBanner daemonId="d-1" />);
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByTestId("machine-health-banner")).toBeNull();

    state.daemons = [machine({ sinceMs: 9_000n })];
    rerender(<MachineHealthBanner daemonId="d-1" />);
    expect(screen.getByTestId("machine-health-banner")).toBeInTheDocument();
  });
});
