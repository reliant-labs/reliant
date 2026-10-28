/**
 * The Add machine sheet: the one way the app adds a machine. These pin the
 * approved behaviour — Small preselected under a unique name, sizes above the
 * plan as upgrade rows, payment in place with ONE explicit create after it
 * (never an automatic billable create), provisioning that hands the new
 * machine back to the caller, and pairing for your own computer.
 */
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ConnectError, Code } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { DaemonSize } from "@/gen/controlplane/controlplane/v1/shared_pb";
import { renderWithQuery } from "@/test/renderWithQuery";

const state = vi.hoisted(() => ({
  daemons: [] as { daemonId: string; name: string; status: number }[],
  allowedSizes: ["small"] as string[],
  hasSubscription: true,
}));
const mockCreateMachine = vi.fn();

vi.mock("@/services/controlPlane/capabilities", () => ({ capabilities: { cloudDaemons: true } }));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ daemons: state.daemons, activeDaemon: undefined, loading: false }),
}));
vi.mock("@/hooks/daemon-registry-queries", () => ({ invalidateDaemonList: vi.fn() }));
vi.mock("@/services/controlPlane/billing", () => ({
  getComputeEligibility: async () => ({ eligible: true, allowedDaemonSizes: state.allowedSizes }),
}));
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  cloudBillingKeys: { computeSubscription: ["sub"] },
  useComputeSubscription: () => ({ data: state.hasSubscription ? { subscription: { plan: { id: "plan-small" } } } : {} }),
  usePlans: () => ({ isLoading: false, data: { plans: [] } }),
}));
// The catalog, already derived to one row per size.
vi.mock("@/components/Billing/machineOptions", () => ({
  deriveMachineOptions: () => [
    { planId: "plan-small", label: "Small", detail: "2 GB", size: "small", monthlyPriceCents: 1900 },
    { planId: "plan-medium", label: "Medium", detail: "4 GB", size: "medium", monthlyPriceCents: 3900 },
  ],
}));
vi.mock("@/components/Settings/cloud/createMachine", () => ({
  createMachine: (args: unknown) => mockCreateMachine(args),
}));
vi.mock("@/hooks/useAccountGate", () => ({
  useAccountGate: () => ({ run: async (fn: () => unknown) => fn(), handleError: () => false, modal: null }),
}));
vi.mock("@/components/Billing/ComputeSubscriptionCheckout", () => ({
  ComputeSubscriptionCheckout: ({ onDone, selectedPlanId }: { onDone: () => void; selectedPlanId: string }) => (
    <button type="button" onClick={onDone}>
      Pay for {selectedPlanId}
    </button>
  ),
}));
vi.mock("../PairMachinePanel", () => ({
  PairMachinePanel: ({ onConnected }: { onConnected: (id: string) => void }) => (
    <button type="button" onClick={() => onConnected("d-self")}>
      Approve my laptop
    </button>
  ),
}));
vi.mock("@/components/DaemonWaitState", () => ({
  DaemonWaitState: ({ state: s }: { state: { title: string } }) => <p>{s.title}</p>,
}));
vi.mock("@/components/forge-ui/modal", () => ({
  default: ({ open, title, children, footer }: { open: boolean; title: string; children: React.ReactNode; footer?: React.ReactNode }) =>
    open ? (
      <div role="dialog" aria-label={title}>
        {children}
        {footer}
      </div>
    ) : null,
}));

import { AddMachineSheet } from "../AddMachineSheet";

beforeEach(() => {
  state.daemons = [{ daemonId: "d-1", name: "cloud-small", status: DaemonStatus.ACTIVE }];
  state.allowedSizes = ["small"];
  state.hasSubscription = true;
  mockCreateMachine.mockReset().mockResolvedValue({ daemonId: "d-new", machineName: "cloud-small-2", repo: { kind: "none" } });
});

describe("AddMachineSheet", () => {
  it("preselects Small under a free name and creates it, then hands the machine back when it is ready", async () => {
    const onReady = vi.fn();
    const onClose = vi.fn();
    const user = userEvent.setup();
    const { rerender } = renderWithQuery(<AddMachineSheet open onClose={onClose} onReady={onReady} />);

    const create = await screen.findByRole("button", { name: "Create cloud-small-2" });
    expect(screen.getByRole("radio", { name: /Small/ })).toHaveAttribute("aria-checked", "true");
    await user.click(create);

    expect(mockCreateMachine).toHaveBeenCalledWith({
      machine: { name: "cloud-small-2", size: DaemonSize.DAEMON_SIZE_SMALL, idleTimeout: "30m" },
      repo: null,
    });
    expect(await screen.findByRole("button", { name: "Keep working" })).toBeInTheDocument();
    expect(onReady).not.toHaveBeenCalled();

    // The machine attaches.
    state.daemons = [...state.daemons, { daemonId: "d-new", name: "cloud-small-2", status: DaemonStatus.ACTIVE }];
    rerender(<AddMachineSheet open onClose={onClose} onReady={onReady} />);
    await waitFor(() => expect(onReady).toHaveBeenCalledWith({ daemonId: "d-new", name: "cloud-small-2" }));
    expect(onClose).toHaveBeenCalled();
  });

  it.each([false, true])("finishes a delayed create after dismissal (reopen: %s) without duplicate creates", async (reopen) => {
    let resolveCreate!: (result: unknown) => void;
    const pending = new Promise((resolve) => { resolveCreate = resolve; });
    mockCreateMachine.mockReturnValue(pending);
    const onReady = vi.fn();
    const onClose = vi.fn();
    const user = userEvent.setup();
    const { rerender } = renderWithQuery(<AddMachineSheet open onClose={onClose} onReady={onReady} />);
    await user.click(await screen.findByRole("button", { name: "Create cloud-small-2" }));
    expect(screen.getByText("Creating cloud-small-2…")).toBeInTheDocument();

    // Model the owner's response to any dismissal (Escape, X, backdrop).
    rerender(<AddMachineSheet open={false} onClose={onClose} onReady={onReady} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    if (reopen) {
      rerender(<AddMachineSheet open onClose={onClose} onReady={onReady} />);
      expect(screen.getByText("Creating cloud-small-2…")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Create cloud-small-2" })).not.toBeInTheDocument();
      rerender(<AddMachineSheet open={false} onClose={onClose} onReady={onReady} />);
    }

    await act(async () => {
      resolveCreate({ daemonId: "d-new", machineName: "cloud-small-2", repo: { kind: "none" } });
      await pending;
    });
    expect(onReady).not.toHaveBeenCalled();
    state.daemons = [...state.daemons, { daemonId: "d-new", name: "cloud-small-2", status: DaemonStatus.ACTIVE }];
    rerender(<AddMachineSheet open={false} onClose={onClose} onReady={onReady} />);
    expect(onReady).toHaveBeenCalledExactlyOnceWith({ daemonId: "d-new", name: "cloud-small-2" });
    rerender(<AddMachineSheet open={false} onClose={onClose} onReady={onReady} />);
    expect(onReady).toHaveBeenCalledOnce();
    expect(mockCreateMachine).toHaveBeenCalledOnce();
  });

  it("shows a size above the plan as an upgrade, takes payment in place, and creates only on an explicit click", async () => {
    const user = userEvent.setup();
    renderWithQuery(<AddMachineSheet open onClose={vi.fn()} />);

    expect(await screen.findByText("upgrades your plan")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: /Medium/ }));
    await user.click(screen.getByRole("button", { name: "Continue to payment" }));
    await user.click(screen.getByRole("button", { name: "Pay for plan-medium" }));

    // Paid: nothing billable has fired on its own.
    expect(await screen.findByTestId("add-machine-paid")).toBeInTheDocument();
    expect(mockCreateMachine).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Create cloud-medium" }));
    expect(mockCreateMachine).toHaveBeenCalledWith(
      expect.objectContaining({ machine: expect.objectContaining({ size: DaemonSize.DAEMON_SIZE_MEDIUM }) }),
    );
  });

  it("takes the next free name when a generated one was taken a moment ago", async () => {
    mockCreateMachine
      .mockRejectedValueOnce(new ConnectError("exists", Code.AlreadyExists))
      .mockResolvedValueOnce({ daemonId: "d-new", machineName: "cloud-small-3", repo: { kind: "none" } });
    const user = userEvent.setup();
    renderWithQuery(<AddMachineSheet open onClose={vi.fn()} />);
    await user.click(await screen.findByRole("button", { name: "Create cloud-small-2" }));
    await waitFor(() => expect(mockCreateMachine).toHaveBeenCalledTimes(2));
    expect(mockCreateMachine.mock.calls[1][0].machine.name).toBe("cloud-small-3");
  });

  it("pairs your own computer and hands it back", async () => {
    const onReady = vi.fn();
    const user = userEvent.setup();
    renderWithQuery(<AddMachineSheet open onClose={vi.fn()} onReady={onReady} />);
    await user.click(await screen.findByRole("radio", { name: /Your own computer/ }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await act(async () => {
      await user.click(screen.getByRole("button", { name: "Approve my laptop" }));
    });
    expect(onReady).toHaveBeenCalledWith({ daemonId: "d-self", name: "Your machine" });
  });
});
