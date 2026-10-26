import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { RecoveryInboxItemSchema } from "@/gen/reliant/v1/inbox_pb";
import { RecoveryAction } from "../RecoveryAction";

const api = vi.hoisted(() => ({ restore: vi.fn(), remove: vi.fn(), machines: vi.fn(), refetch: vi.fn() }));
vi.mock("@/hooks/useOnboardingQueries", () => ({ useDaemonList: api.machines }));
vi.mock("@/hooks/worktree-actions", () => ({ restoreWorktreeRecovery: api.restore, deleteWorktreeRecovery: api.remove }));
const recovery = (overrides: MessageInitShape<typeof RecoveryInboxItemSchema> = {}) => create(RecoveryInboxItemSchema, { recoveryId: "recovery", phase: "removed", checkoutCount: 2, lastError: "One remote is offline", ...overrides });
const machine = (daemonId: string, status = DaemonStatus.ACTIVE) => create(DaemonInfoSchema, { daemonId, name: daemonId === "target" ? "Target machine" : daemonId, status });
const expand = () => fireEvent.click(screen.getByRole("button", { name: "Recovery actions" }));
const selectTarget = () => fireEvent.change(screen.getByLabelText("Destination machine"), { target: { value: "target" } });

describe("retained recovery actions", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    api.machines.mockReturnValue({ data: [machine("target"), machine("old-source", DaemonStatus.DISCONNECTED)], isError: false, isPending: false, refetch: api.refetch });
  });
  it("restores separately with stable retry identity and retains branches", async () => {
    api.restore.mockResolvedValueOnce({ recovery: { phase: "held", lastError: "Try again" } }).mockResolvedValueOnce({ recovery: { phase: "restored" }, worktreeId: "new-workspace" });
    render(<RecoveryAction recovery={recovery()} />);
    fireEvent.click(screen.getByText("Recovery actions"));
    expect(screen.getByText(/visible to repository collaborators/)).toBeTruthy();
    expect(screen.getByText(/One remote is offline/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Destination machine"), { target: { value: "target" } });
    fireEvent.click(screen.getByText("Restore separately"));
    await screen.findByText("Try again");
    fireEvent.click(screen.getByText("Retry restore"));
    await screen.findByText("new-workspace");
    expect(api.restore).toHaveBeenCalledTimes(2);
    expect(api.restore.mock.calls[1]).toEqual(api.restore.mock.calls[0]);
    expect(api.remove).not.toHaveBeenCalled();
    expect(screen.getByText(/branches are still retained/)).toBeTruthy();
  });
  it("requires explicit permanent deletion confirmation, never dismiss-as-restore", async () => {
    api.remove.mockResolvedValue({ recovery: { phase: "refs_deleted" } });
    render(<RecoveryAction recovery={recovery()} />);
    fireEvent.click(screen.getByText("Recovery actions"));
    fireEvent.click(screen.getByText("Delete recovery branches…"));
    expect(api.remove).not.toHaveBeenCalled();
    expect(screen.getByText(/last recoverable copy/)).toBeTruthy();
    fireEvent.click(screen.getByText("Confirm permanent recovery deletion"));
    await waitFor(() => expect(api.remove).toHaveBeenCalledTimes(1));
    expect(api.restore).not.toHaveBeenCalled();
    await screen.findByText(/Exact recovery branches deleted/);
    expect(screen.queryByRole("button", { name: "Restore separately" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Confirm permanent recovery deletion" })).not.toBeInTheDocument();
  });

  it("only offers connected machines, without assuming the source machine still exists", () => {
    render(<RecoveryAction recovery={recovery()} />);
    expect(api.machines).toHaveBeenLastCalledWith({ enabled: false });
    expect(screen.getByRole("button", { name: "Recovery actions" })).toHaveAttribute("aria-expanded", "false");
    expand();
    expect(api.machines).toHaveBeenLastCalledWith({ enabled: true });
    expect(screen.getByRole("button", { name: "Recovery actions" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(/original machine is not required/)).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "old-source" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore separately" })).toBeDisabled();
    selectTarget();
    expect(screen.getByRole("button", { name: "Restore separately" })).toBeEnabled();
  });

  it("resumes the persisted operation and destination after remount", async () => {
    api.restore.mockResolvedValue({ recovery: { phase: "held", lastError: "Still held" } });
    const saved = recovery({ phase: "held", operation: "restoring", operationKey: "durable-key", targetDaemonId: "target" });
    const first = render(<RecoveryAction recovery={saved} />);
    expand();
    expect(screen.getByLabelText("Destination machine")).toHaveValue("target");
    expect(screen.getByLabelText("Destination machine")).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Retry restore" }));
    await screen.findByText("Still held");
    expect(api.restore).toHaveBeenCalledWith("recovery", "target", "durable-key");
    first.unmount();
    render(<RecoveryAction recovery={saved} />);
    expand();
    fireEvent.click(screen.getByRole("button", { name: "Retry restore" }));
    await screen.findByText("Still held");
    expect(api.restore.mock.calls[1]).toEqual(api.restore.mock.calls[0]);
    expect(screen.getByRole("button", { name: "Delete recovery branches…" })).toBeDisabled();
  });

  it("retains operation identity on transport failure and prevents an opposite operation", async () => {
    api.restore.mockRejectedValueOnce(new Error("Network interrupted")).mockResolvedValueOnce({ recovery: { phase: "restored" }, worktreeId: "recovered" });
    render(<RecoveryAction recovery={recovery()} />);
    expand();
    selectTarget();
    fireEvent.click(screen.getByRole("button", { name: "Restore separately" }));
    await screen.findByText("Network interrupted");
    expect(screen.getByRole("button", { name: "Delete recovery branches…" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Retry restore" }));
    await screen.findByText("recovered");
    expect(api.restore.mock.calls[1]).toEqual(api.restore.mock.calls[0]);
    expect(screen.queryByRole("button", { name: "Retry restore" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete recovery branches…" })).toBeEnabled();
  });

  it("retries partially completed deletion with its persisted key, never restores", async () => {
    api.remove.mockResolvedValue({ recovery: { phase: "held", lastError: "One branch remains" } });
    render(<RecoveryAction recovery={recovery({ phase: "held", operation: "deletion_requested", operationKey: "delete-key" })} />);
    expand();
    expect(screen.getByRole("button", { name: "Restore separately" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Retry recovery deletion…" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm permanent recovery deletion" }));
    await screen.findByText("One branch remains");
    fireEvent.click(screen.getByRole("button", { name: "Confirm permanent recovery deletion" }));
    await waitFor(() => expect(api.remove).toHaveBeenCalledTimes(2));
    expect(api.remove.mock.calls).toEqual([["recovery", "delete-key"], ["recovery", "delete-key"]]);
    expect(api.restore).not.toHaveBeenCalled();
  });

  it("canceling deletion and dismissing the action panel never deletes refs", () => {
    render(<RecoveryAction recovery={recovery()} />);
    expand();
    fireEvent.click(screen.getByRole("button", { name: "Delete recovery branches…" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expand();
    expect(api.remove).not.toHaveBeenCalled();
    expect(api.restore).not.toHaveBeenCalled();
  });

  it("does not claim an unverified capture is a backup or allow destructive actions", () => {
    render(<RecoveryAction recovery={recovery({ phase: "publishing" })} />);
    expand();
    expect(screen.getByText(/not.*verified backup/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore separately" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Delete recovery branches…" })).toBeDisabled();
  });

  it("explains held state and unavailable machines without losing recovery", () => {
    api.machines.mockReturnValue({ data: [], isPending: false, isError: false });
    render(<RecoveryAction recovery={recovery({ phase: "held" })} />);
    expand();
    expect(screen.getByText(/held, not confirmed complete/)).toBeInTheDocument();
    expect(screen.getByText(/Connect a machine and install this project/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore separately" })).toBeDisabled();
  });

  it("does not act on stale machine data when the list fails and offers retry", () => {
    api.machines.mockReturnValue({ data: [machine("target")], isPending: false, isError: true, refetch: api.refetch });
    render(<RecoveryAction recovery={recovery()} />);
    expand();
    selectTarget();
    expect(screen.getByRole("button", { name: "Restore separately" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Retry machine list" }));
    expect(api.refetch).toHaveBeenCalledOnce();
  });
});
