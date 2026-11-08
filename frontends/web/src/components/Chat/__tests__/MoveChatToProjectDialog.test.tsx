/**
 * "Move to project…" (MoveChatToProject): the picker offers the user's other
 * projects with where the chat would run in each, moves the chat, and says up
 * front what the server would refuse (a busy chat, a project set up nowhere)
 * or require (leaving a branch workspace behind).
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChatActivity } from "@/gen/reliant/v1/chat_pb";
import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";

const state = vi.hoisted(() => ({
  chat: undefined as Record<string, unknown> | undefined,
  activeChatId: "chat-1" as string | null,
}));
const mocks = vi.hoisted(() => ({
  mutateAsync: vi.fn(async (args: { chatId: string; projectId: string }) => ({ id: args.chatId, projectId: args.projectId })),
  openChat: vi.fn(async () => {}),
  toastSuccess: vi.fn(),
}));

vi.mock("@/hooks/chat-queries", () => ({
  useChat: () => ({ data: state.chat }),
  useMoveChatToProject: () => ({ mutateAsync: mocks.mutateAsync, isPending: false }),
}));
vi.mock("@/hooks/project-queries", () => ({
  useProjects: () => ({
    projects: [
      { id: "p-billing", name: "billing" },
      { id: "p-properties", name: "properties" },
      { id: "p-elsewhere", name: "elsewhere" },
      { id: "p-cloning", name: "cloning" },
    ],
    isLoading: false,
  }),
  useProjectDaemonInstalls: () => ({
    data: [
      { project_id: "p-properties", daemon_id: "laptop", install_state: ProjectInstallState.INSTALLED },
      { project_id: "p-elsewhere", daemon_id: "desktop", install_state: ProjectInstallState.INSTALLED },
      { project_id: "p-cloning", daemon_id: "desktop", install_state: ProjectInstallState.INSTALLING },
    ],
  }),
}));
vi.mock("@/store/chatStore", () => ({
  useChatStore: Object.assign(vi.fn(), { getState: () => ({ activeChatId: state.activeChatId }) }),
}));
vi.mock("@/store/openChat", () => ({ openChat: mocks.openChat }));
vi.mock("@/lib/toast-manager", () => ({
  toast: { success: mocks.toastSuccess, error: vi.fn(), info: vi.fn(), notify: vi.fn() },
}));

const { MoveChatToProjectDialog, moveTarget } = await import("../MoveChatToProjectDialog");

beforeEach(() => {
  vi.clearAllMocks();
  state.activeChatId = "chat-1";
  state.chat = {
    id: "chat-1",
    projectId: "p-billing",
    activeDaemonId: "laptop",
    activity: ChatActivity.IDLE,
    worktree: { id: "wt-main", isMain: true, name: "main", branch: "main" },
  };
});

describe("MoveChatToProjectDialog", () => {
  it("offers the other projects, with where the chat would run in each", () => {
    render(<MoveChatToProjectDialog chatId="chat-1" open onClose={vi.fn()} />);
    const radios = screen.getAllByRole("radio");
    expect(radios.map((r) => r.textContent)).toEqual([
      "propertiesOn this chat's machine",
      "elsewhereMoves to a machine that has it",
      "cloningNot set up on any of your machines yet",
    ]);
    expect(screen.getByRole("radio", { name: /cloning/ })).toBeDisabled();
  });

  it("moves the chat and follows it when it is the open chat", async () => {
    const onClose = vi.fn();
    render(<MoveChatToProjectDialog chatId="chat-1" open onClose={onClose} />);
    expect(screen.getByTestId("move-chat-confirm")).toBeDisabled();

    fireEvent.click(screen.getByRole("radio", { name: /properties/ }));
    fireEvent.click(screen.getByTestId("move-chat-confirm"));

    await waitFor(() =>
      expect(mocks.mutateAsync).toHaveBeenCalledWith({ chatId: "chat-1", projectId: "p-properties", leaveWorkspace: false }),
    );
    expect(onClose).toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Moved to properties");
    expect(mocks.openChat).toHaveBeenCalledWith({ id: "chat-1", projectId: "p-properties" });
  });

  it("leaves a branch workspace only when the user confirms that", async () => {
    state.chat = {
      ...state.chat,
      worktree: { id: "wt-fix", isMain: false, name: "fix", branch: "fix-invoices" },
    };
    render(<MoveChatToProjectDialog chatId="chat-1" open onClose={vi.fn()} />);
    expect(screen.getByTestId("move-chat-workspace-notice")).toHaveTextContent("fix-invoices");
    expect(screen.getByTestId("move-chat-workspace-notice")).toHaveTextContent("stays with billing");

    fireEvent.click(screen.getByRole("radio", { name: /properties/ }));
    const confirm = screen.getByTestId("move-chat-confirm");
    expect(confirm).toHaveTextContent("Leave workspace and move");
    fireEvent.click(confirm);
    await waitFor(() =>
      expect(mocks.mutateAsync).toHaveBeenCalledWith({ chatId: "chat-1", projectId: "p-properties", leaveWorkspace: true }),
    );
  });

  it("does not offer to move a busy chat", () => {
    state.chat = { ...state.chat, activity: ChatActivity.RUNNING };
    render(<MoveChatToProjectDialog chatId="chat-1" open onClose={vi.fn()} />);
    fireEvent.click(screen.getByRole("radio", { name: /properties/ }));
    expect(screen.getByRole("status")).toHaveTextContent("busy");
    expect(screen.getByTestId("move-chat-confirm")).toBeDisabled();
  });

  it("shows the server's refusal and stays open", async () => {
    mocks.mutateAsync.mockRejectedValueOnce(new Error("this chat is running"));
    const onClose = vi.fn();
    render(<MoveChatToProjectDialog chatId="chat-1" open onClose={onClose} />);
    fireEvent.click(screen.getByRole("radio", { name: /properties/ }));
    fireEvent.click(screen.getByTestId("move-chat-confirm"));
    expect(await screen.findByText("this chat is running")).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("moveTarget", () => {
  it("accepts any machine for a project installed nowhere, and keeps a no-machine chat without one", () => {
    expect(moveTarget([], "p", { activeDaemonId: "laptop" })).toEqual({ movable: true, hint: "Not set up on a machine yet" });
    expect(moveTarget([], "p", { noMachine: true }).hint).toBe("Stays without a machine");
  });
});
