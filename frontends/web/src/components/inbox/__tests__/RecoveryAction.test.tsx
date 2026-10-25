import { create } from "@bufbuild/protobuf";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RecoveryInboxItemSchema } from "@/gen/reliant/v1/inbox_pb";
import { RecoveryAction } from "../RecoveryAction";

const api = vi.hoisted(() => ({ restore: vi.fn(), remove: vi.fn() }));
vi.mock("@/hooks/useOnboardingQueries", () => ({ useDaemonList: () => ({ data: [{ daemonId: "target", name: "Target machine" }], isError: false }) }));
vi.mock("@/hooks/worktree-actions", () => ({ restoreWorktreeRecovery: api.restore, deleteWorktreeRecovery: api.remove }));
const recovery = () => create(RecoveryInboxItemSchema, { recoveryId: "recovery", phase: "removed", checkoutCount: 2, lastError: "One remote is offline" });

describe("retained recovery actions", () => {
  beforeEach(() => { vi.clearAllMocks(); });
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
  });
});
