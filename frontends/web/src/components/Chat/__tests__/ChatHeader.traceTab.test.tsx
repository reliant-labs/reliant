/**
 * The chat header's Trace tab: the owner and a viewer of a shared chat both
 * get it (the viewer's trace is in its viewer shape), a new chat has none,
 * and a host that does not handle the view shows none.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ResourceVisibility } from "../../../gen/reliant/v1/common_pb";

const state = vi.hoisted(() => ({ chat: {} as Record<string, unknown> }));

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
vi.mock("../../Sharing/ShareChatDialog", () => ({
  ShareChatDialog: ({ open }: { open: boolean }) => (open ? <div role="dialog">share dialog</div> : null),
}));

const { ChatHeader } = await import("../ChatHeader");

function renderHeader(props: Partial<Parameters<typeof ChatHeader>[0]> = {}) {
  return render(
    <SurfaceProvider surface="desktop">
      <ChatHeader chatId="chat-1" {...props} />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  state.chat = { id: "chat-1", title: "Deploy fix", orgId: "acme", visibility: ResourceVisibility.PRIVATE, worktreeId: "wt-1" };
});

describe("ChatHeader Trace tab", () => {
  it("gives the owner a Chat / Trace switch", async () => {
    const onViewChange = vi.fn();
    renderHeader({ view: "chat", onViewChange });
    expect(screen.getByRole("tab", { name: "Chat" })).toHaveAttribute("aria-selected", "true");
    await userEvent.setup().click(screen.getByRole("tab", { name: "Trace" }));
    expect(onViewChange).toHaveBeenCalledWith("trace");
  });

  it("gives a viewer of a shared chat the switch too", () => {
    state.chat = { ...state.chat, visibility: ResourceVisibility.ORG, viewerAccess: true, userId: "dana" };
    renderHeader({ view: "trace", onViewChange: vi.fn() });
    expect(screen.getByRole("tab", { name: "Trace" })).toHaveAttribute("aria-selected", "true");
  });

  it("has no switch for a new chat", () => {
    renderHeader({ chatId: null, isNewChat: true, onViewChange: vi.fn() });
    expect(screen.queryByTestId("chat-view-tabs")).not.toBeInTheDocument();
  });

  it("has no switch when the host does not handle the view", () => {
    renderHeader();
    expect(screen.queryByTestId("chat-view-tabs")).not.toBeInTheDocument();
  });
});
