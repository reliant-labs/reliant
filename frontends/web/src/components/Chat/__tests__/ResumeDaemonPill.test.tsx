import { screen, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonStatus } from "../../../gen/reliant/v1/daemon_registry_pb";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { ResumeDaemonPill } from "../ResumeDaemonPill";

const state = vi.hoisted(() => ({
  status: 0 as number,
  resume: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    daemonRegistry: () => ({
      listDaemons: async () => ({
        daemons: [
          {
            daemonId: "d-1",
            hostname: "ws-1",
            status: state.status,
          },
        ],
      }),
    }),
  },
}));
vi.mock("@/services/controlPlane/daemon", () => ({
  resumeDaemon: (id: string) => state.resume(id),
  createDaemon: vi.fn(),
  suspendDaemon: vi.fn(),
  deleteDaemon: vi.fn(),
}));
vi.mock("@/hooks/useGoToBilling", () => ({ useGoToBilling: () => vi.fn() }));

beforeEach(() => {
  sessionStorage.clear();
  state.status = DaemonStatus.SUSPENDED;
  state.resume.mockReset();
});

describe("ResumeDaemonPill", () => {
  it("hides the pill after a successful resume even while the list still says suspended", async () => {
    state.resume.mockResolvedValue(undefined);
    renderWithQuery(<ResumeDaemonPill placement="inline" />);

    fireEvent.click(await screen.findByText("Resume ws-1"));

    await waitFor(() => expect(state.resume).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.queryByText("Resume ws-1")).toBeNull());
    expect(screen.queryByText(/Resuming/)).toBeNull();
  });

  it("treats 'daemon is not suspended' as success: no error box, pill gone", async () => {
    state.resume.mockRejectedValue(new ConnectError("daemon is not suspended", Code.FailedPrecondition));
    renderWithQuery(<ResumeDaemonPill placement="inline" />);

    fireEvent.click(await screen.findByText("Resume ws-1"));

    await waitFor(() => expect(screen.queryByText("Resume ws-1")).toBeNull());
    expect(screen.queryByText(/not suspended/)).toBeNull();
    expect(screen.queryByText("Upgrade plan")).toBeNull();
  });

  it("shows the error but NOT 'Upgrade plan' for a non-billing failed_precondition", async () => {
    state.resume.mockRejectedValue(new ConnectError("cannot resume external daemon", Code.FailedPrecondition));
    renderWithQuery(<ResumeDaemonPill placement="inline" />);

    fireEvent.click(await screen.findByText("Resume ws-1"));

    expect(await screen.findByText(/cannot resume external daemon/)).toBeTruthy();
    expect(screen.queryByText("Upgrade plan")).toBeNull();
  });

  it("shows 'Upgrade plan' when the server tags the refusal with an entitlement reason", async () => {
    const err = new ConnectError(
      "no compute subscription",
      Code.FailedPrecondition,
      new Headers({ "x-reliant-reason": "no_compute_subscription" }),
    );
    state.resume.mockRejectedValue(err);
    renderWithQuery(<ResumeDaemonPill placement="inline" />);

    fireEvent.click(await screen.findByText("Resume ws-1"));

    expect(await screen.findByText("Upgrade plan")).toBeTruthy();
  });
});
