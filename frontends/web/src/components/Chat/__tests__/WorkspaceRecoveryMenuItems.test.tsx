import { fireEvent, screen, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Worktree } from "../../../store/worktreeStore";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { WorkspaceRecoveryMenuItems } from "../WorkspaceRecoveryMenuItems";

const state = vi.hoisted(() => ({
  recreate: vi.fn(),
  update: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock("@/api/worktree-grpc", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  worktreeGrpc: { recreate: (id: string) => state.recreate(id) },
}));
vi.mock("@/api/client", () => ({
  api: { chatsV2: { update: (chatId: string, updates: unknown) => state.update(chatId, updates) } },
}));
vi.mock("@/lib/toast-manager", () => ({
  toast: { success: state.success, error: state.error },
}));

function worktree(overrides: Partial<Worktree> = {}): Worktree {
  return {
    id: "wt-errors",
    name: "errors",
    path: "/home/workspace/.reliant/worktrees/reliant-labs/errors-5972f1d1",
    branch: "fix/errors",
    base_branch: "main",
    project_id: "proj-1",
    status: WorktreeStatus.ACTIVE,
    is_main: false,
    created_at: "",
    updated_at: "",
    last_active: "",
    ...overrides,
  };
}

const main = worktree({ id: "wt-main", name: "main", branch: "main", path: "/home/workspace/projects/reliant-labs", is_main: true });

beforeEach(() => {
  state.recreate.mockReset();
  state.update.mockReset();
  state.success.mockReset();
  state.error.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("WorkspaceRecoveryMenuItems", () => {
  it("recreates the chat's workspace and reports what the server did", async () => {
    state.recreate.mockResolvedValue({ message: "Workspace recreated from branch fix/errors. Uncommitted changes that were in it are gone." });
    const onSelect = vi.fn();
    renderWithQuery(<WorkspaceRecoveryMenuItems chatId="chat-1" worktree={worktree()} mainWorktree={main} onSelect={onSelect} />);

    fireEvent.click(screen.getByText("Recreate workspace"));

    await waitFor(() => expect(state.recreate).toHaveBeenCalledWith("wt-errors"));
    await waitFor(() => expect(state.success).toHaveBeenCalledWith(expect.stringContaining("recreated from branch fix/errors"), expect.anything()));
    expect(onSelect).toHaveBeenCalled();
  });

  it("shows the server's reason when the workspace cannot be recreated", async () => {
    const refusal = new ConnectError("could not recreate the workspace: branch 'fix/errors' no longer exists", Code.FailedPrecondition);
    state.recreate.mockRejectedValue(refusal);
    renderWithQuery(<WorkspaceRecoveryMenuItems chatId="chat-1" worktree={worktree()} mainWorktree={main} />);

    fireEvent.click(screen.getByText("Recreate workspace"));

    await waitFor(() => expect(state.error).toHaveBeenCalledWith(refusal, expect.anything()));
    expect(state.success).not.toHaveBeenCalled();
  });

  it("moves the chat to the project's main checkout after confirming", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    state.update.mockResolvedValue({ id: "chat-1" });
    renderWithQuery(<WorkspaceRecoveryMenuItems chatId="chat-1" worktree={worktree()} mainWorktree={main} />);

    fireEvent.click(screen.getByText("Move to main checkout"));

    expect(confirmSpy.mock.calls[0][0]).toContain("/home/workspace/projects/reliant-labs");
    await waitFor(() => expect(state.update).toHaveBeenCalledWith("chat-1", { worktree_id: "wt-main" }));
    await waitFor(() => expect(state.success).toHaveBeenCalled());
  });

  it("does nothing when the move is not confirmed", () => {
    vi.spyOn(window, "confirm").mockReturnValue(false);
    renderWithQuery(<WorkspaceRecoveryMenuItems chatId="chat-1" worktree={worktree()} mainWorktree={main} />);

    fireEvent.click(screen.getByText("Move to main checkout"));

    expect(state.update).not.toHaveBeenCalled();
  });

  it("asks the server for the main checkout when its id is not known", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    state.update.mockResolvedValue({ id: "chat-1" });
    renderWithQuery(<WorkspaceRecoveryMenuItems chatId="chat-1" worktree={worktree()} />);

    fireEvent.click(screen.getByText("Move to main checkout"));

    await waitFor(() => expect(state.update).toHaveBeenCalledWith("chat-1", { worktree_id: "" }));
  });

  it("offers nothing for a chat that is already on the main checkout", () => {
    renderWithQuery(<WorkspaceRecoveryMenuItems chatId="chat-1" worktree={main} mainWorktree={main} />);

    expect(screen.queryByText("Recreate workspace")).toBeNull();
    expect(screen.queryByText("Move to main checkout")).toBeNull();
  });
});
