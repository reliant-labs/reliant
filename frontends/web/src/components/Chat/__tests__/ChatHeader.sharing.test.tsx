/**
 * The chat header and sharing: the owner of an org's chat can share it (and
 * sees it is shared); a viewer gets a read-only header with nothing that
 * changes the chat.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ResourceVisibility } from "../../../gen/reliant/v1/common_pb";

const state = vi.hoisted(() => ({ chat: {} as Record<string, unknown> }));

vi.mock("../../../services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("../thread-views/useChatThreadMode", () => ({
  useChatThreadMode: () => ({ mode: "detailed", preference: "auto" }),
  setChatThreadMode: vi.fn(),
}));
vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: state.chat }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../../hooks/message-queries", () => ({ useMessages: () => ({ data: { messages: [] } }) }));
vi.mock("../../../hooks/chat-actions", () => ({ compactChat: vi.fn() }));
vi.mock("../../../hooks/worktree-queries", () => ({
  resolveWorktree: async () => null,
  getCachedMainWorktree: () => undefined,
}));
vi.mock("../../../hooks/worktree-actions", () => ({ loadMainWorktree: vi.fn() }));
vi.mock("../../../lib/toast-manager", () => ({
  toast: { notify: vi.fn(), success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));
vi.mock("../../../store/chatStoreHooks", () => ({
  useContextUsage: () => null,
  useContextUsageByThread: () => ({}),
}));
vi.mock("../thread-views", () => ({ useThreads: () => [], ThreadTabs: () => null }));
vi.mock("../../runs/ChildRunsStrip", () => ({ ChildRunsStrip: () => null }));
vi.mock("../NoMachine", () => ({ NoMachinePill: () => <span>no machine</span> }));
vi.mock("../WorkspaceRecoveryMenuItems", () => ({ WorkspaceRecoveryMenuItems: () => null }));
vi.mock("../MoveChatToProjectDialog", () => ({ MoveChatToProjectDialog: () => null }));
vi.mock("../../Sharing/ShareChatDialog", () => ({
  ShareChatDialog: ({ open }: { open: boolean }) => (open ? <div role="dialog">share dialog</div> : null),
}));

const { ChatHeader } = await import("../ChatHeader");

async function openMenu() {
  const user = userEvent.setup();
  render(
    <SurfaceProvider surface="desktop">
      <ChatHeader chatId="chat-1" />
    </SurfaceProvider>,
  );
  await user.click(screen.getByRole("button", { name: "More options" }));
  return user;
}

beforeEach(() => {
  state.chat = { id: "chat-1", title: "Deploy fix", orgId: "acme", visibility: ResourceVisibility.PRIVATE, worktreeId: "wt-1" };
});

describe("ChatHeader sharing", () => {
  it("offers the owner of an org's chat to share it", async () => {
    const user = await openMenu();
    await user.click(screen.getByTestId("chat-menu-share"));
    expect(screen.getByRole("dialog")).toHaveTextContent("share dialog");
    expect(screen.queryByTestId("chat-shared-chip")).not.toBeInTheDocument();
  });

  it("shows the owner a shared chat is shared, and offers to stop", async () => {
    state.chat = { ...state.chat, visibility: ResourceVisibility.ORG };
    await openMenu();
    expect(screen.getByTestId("chat-shared-chip")).toHaveTextContent("Shared");
    expect(screen.getByTestId("chat-menu-share")).toHaveTextContent("Stop sharing…");
  });

  it("offers no share for a chat of no org", async () => {
    state.chat = { ...state.chat, orgId: undefined };
    await openMenu();
    expect(screen.queryByTestId("chat-menu-share")).not.toBeInTheDocument();
  });

  it("gives a viewer a read-only header: nothing that changes the chat", async () => {
    state.chat = { ...state.chat, visibility: ResourceVisibility.ORG, viewerAccess: true, userId: "dana", noMachine: true };
    await openMenu();
    expect(screen.getByTestId("chat-shared-chip")).toHaveTextContent("Read only");
    for (const name of ["Edit Title", "Move to project…", "Export Chat", "Archive chat"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
    expect(screen.queryByTestId("chat-menu-share")).not.toBeInTheDocument();
    expect(screen.queryByText("no machine")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy Chat ID" })).toBeInTheDocument();
  });
});
