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

const state = vi.hoisted(() => ({
  chat: {} as Record<string, unknown>,
  members: undefined as Record<string, unknown> | undefined,
}));

vi.mock("../../../hooks/useDaemonStatus", () => ({ useDaemonStatus: () => ({ daemons: [], activeDaemon: null, loading: false }) }));
vi.mock("../../../services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("../../../hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: [
    { daemonId: "other", name: "Other machine" },
    { daemonId: "assigned", name: "Build workstation" },
  ] }),
}));
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
vi.mock("../../../hooks/run-queries", () => ({ useChildRuns: () => ({ data: undefined }) }));
vi.mock("../NoMachine", () => ({ NoMachinePill: () => <span>no machine</span> }));
vi.mock("../WorkspaceRecoveryMenuItems", () => ({ WorkspaceRecoveryMenuItems: () => null }));
vi.mock("../MoveChatToProjectDialog", () => ({ MoveChatToProjectDialog: () => null }));
vi.mock("../../../hooks/chat-member-queries", () => ({ useChatMembers: () => ({ data: state.members }) }));
vi.mock("../../Sharing/ChatMembersPanel", () => ({
  ChatMembersPanel: ({ open }: { open: boolean }) => (open ? <div role="dialog">members panel</div> : null),
}));
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
  state.members = undefined;
});

describe("ChatHeader machine", () => {
  it("names only the chat's assigned machine", async () => {
    state.chat = { ...state.chat, activeDaemonId: "assigned" };
    await openMenu();
    expect(screen.getByText("Machine: Build workstation")).toBeInTheDocument();
    expect(screen.queryByText("Machine: Other machine")).not.toBeInTheDocument();
  });

  it("doesn't guess when the assigned machine isn't in the registry", async () => {
    state.chat = { ...state.chat, activeDaemonId: "missing" };
    await openMenu();
    expect(screen.queryByText(/^Machine:/)).not.toBeInTheDocument();
  });
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
    for (const name of ["Edit title", "Move to project…", "Export chat", "Archive chat"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
    expect(screen.queryByTestId("chat-menu-share")).not.toBeInTheDocument();
    expect(screen.queryByText("no machine")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy chat ID" })).toBeInTheDocument();
  });
});

describe("ChatHeader members", () => {
  it("offers who has access once the deployment allows grants", async () => {
    state.members = { grantsEnabled: true, memberCount: 0 };
    const user = await openMenu();
    await user.click(screen.getByTestId("chat-menu-members"));
    expect(screen.getByRole("dialog")).toHaveTextContent("members panel");
  });

  it("offers nothing while grants are off and no one holds one, or the server cannot say", async () => {
    state.members = { grantsEnabled: false, memberCount: 0 };
    await openMenu();
    expect(screen.queryByTestId("chat-menu-members")).not.toBeInTheDocument();
  });

  it("still offers it with grants off while someone holds one, so it can be taken back", async () => {
    state.members = { grantsEnabled: false, memberCount: 2 };
    await openMenu();
    expect(screen.getByTestId("chat-menu-members")).toBeInTheDocument();
  });

  it("offers it to a viewer, who may see who else is here and leave", async () => {
    state.chat = { ...state.chat, viewerAccess: true, userId: "dana" };
    state.members = { grantsEnabled: true, memberCount: 1 };
    await openMenu();
    expect(screen.getByTestId("chat-menu-members")).toBeInTheDocument();
  });
});
