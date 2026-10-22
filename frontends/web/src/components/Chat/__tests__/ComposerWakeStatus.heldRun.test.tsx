/**
 * The composer's status line while a run is held for its machine
 * (ChatActivity.WAITING_FOR_DAEMON).
 *
 * Prod, 2026-10-09: the user's only machine crash-looped after a release, and
 * a chat held for it said "Waiting for your machine…" for as long as anyone
 * watched — a wait for something that was never coming, with no way out but
 * guessing. A FAILED machine is a state the user has to act on, so it is
 * named, explained, and given its exits.
 */

import { fireEvent, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonInfoSchema, DaemonStatus, type DaemonInfo } from "../../../gen/reliant/v1/daemon_registry_pb";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { ComposerWakeStatus } from "../ComposerWakeStatus";

const state = vi.hoisted(() => ({
  daemons: [] as DaemonInfo[],
  resume: vi.fn(),
  navigate: vi.fn(),
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: state.daemons, isLoading: false }),
  useResumeDaemon: () => ({ mutate: state.resume, isPending: false }),
}));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => state.navigate }));

function daemon(daemonId: string, name: string, status: DaemonStatus, lastStatusMessage = ""): DaemonInfo {
  return create(DaemonInfoSchema, { daemonId, name, status, lastStatusMessage, daemonType: "managed" });
}

beforeEach(() => {
  state.daemons = [];
  state.resume.mockReset();
  state.navigate.mockReset();
});

describe("ComposerWakeStatus: a run held for its machine", () => {
  it("says an unpinned chat's machine failed to start, why, and what to do", () => {
    state.daemons = [daemon("d-1", "Cloud box", DaemonStatus.FAILED, "workspace container exited 1")];
    renderWithQuery(
      <ComposerWakeStatus sending={false} waitingOnMachine continueWithoutMachine={<button>Continue without machine</button>} />,
    );

    expect(screen.getByText("Your machine failed to start")).toBeInTheDocument();
    expect(screen.getByText("workspace container exited 1")).toBeInTheDocument();
    expect(screen.queryByText(/Waiting for your machine/)).toBeNull();
    expect(screen.getByRole("button", { name: "Continue without machine" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Try again/ }));
    expect(state.resume).toHaveBeenCalledWith("d-1");
    fireEvent.click(screen.getByRole("button", { name: /Manage machines/ }));
    expect(state.navigate).toHaveBeenCalled();
  });

  it("says a pinned chat's machine failed to start rather than calling it offline", () => {
    state.daemons = [daemon("d-1", "Cloud box", DaemonStatus.FAILED)];
    renderWithQuery(<ComposerWakeStatus sending={false} daemonId="d-1" waitingOnMachine />);

    expect(screen.getByText("Your machine failed to start")).toBeInTheDocument();
    expect(screen.queryByText(/is offline/)).toBeNull();
  });

  it("names the machine an unpinned chat is waking", () => {
    state.daemons = [daemon("d-1", "Cloud box", DaemonStatus.PENDING)];
    renderWithQuery(<ComposerWakeStatus sending={false} waitingOnMachine />);
    expect(screen.getByRole("status", { name: "Waking Cloud box…" })).toBeInTheDocument();
  });

  it("falls back to the generic line when there is no machine to name", () => {
    renderWithQuery(<ComposerWakeStatus sending={false} waitingOnMachine />);
    expect(screen.getByRole("status", { name: "Waiting for your machine…" })).toBeInTheDocument();
  });

  it("says nothing for an unpinned chat that is not held", () => {
    state.daemons = [daemon("d-1", "Cloud box", DaemonStatus.FAILED)];
    const { container } = renderWithQuery(<ComposerWakeStatus sending={false} />);
    expect(container).toBeEmptyDOMElement();
  });
});
