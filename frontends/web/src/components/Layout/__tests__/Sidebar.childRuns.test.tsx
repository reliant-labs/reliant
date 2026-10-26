// Copyright (c) 2025 Reliant Labs

/**
 * Decision 4's cost guard: the child-runs query belongs to the OPEN chat's
 * header, never to sidebar rows. A sidebar of N chats must not issue N
 * ListRuns({parent_chat_id}) calls.
 *
 * The real Sidebar renders three chats, one of them selected; the RPC client
 * is the only boundary mocked, so any component the sidebar renders that
 * reaches for child runs would show up as a ListRuns call here.
 */

import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";

import { ChatState } from "../../../gen/reliant/v1/chat_pb";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";
import { useChatStore } from "../../../store/chatStore";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { useProjectStore } from "../../../store/projectStore";

vi.mock("../../forge-ui/tooltip", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("../../forge-ui/context_menu", () => ({ default: () => null }));
vi.mock("../../../hooks/useDebounce", () => ({ useDebounce: <T,>(value: T) => value }));

// RunService is reached through the generated hooks, so it is answered at
// the transport.
const listRuns = vi.fn(async (_request: { parentChatId?: string }) => ({ runs: [], nextPageToken: "" }));
beforeEach(() =>
  setTransport(mockTransport({ "RunService.ListRuns": (request) => listRuns(request as { parentChatId?: string }) })),
);
afterEach(() => setTransport(null));

// What the server sends: each chat carries its workspace.
const workspace = {
  id: "worktree-1",
  name: "Workspace",
  branch: "feature/test",
  path: "/tmp/worktree",
  isMain: false,
  status: WorktreeStatus.ACTIVE,
};

const chats: Array<Record<string, unknown>> = ["chat-1", "chat-2", "chat-3"].map((id) => ({
  id,
  worktree: workspace,
  title: `Chat ${id}`,
  createdAt: "2024-01-01T00:00:00.000Z",
  updatedAt: "2024-01-01T00:00:00.000Z",
  lastMessageAt: "2024-01-01T00:00:00.000Z",
  unread: false,
  state: ChatState.IDLE,
  worktreeId: "worktree-1",
  projectId: "project-1",
}));

// Stable, as a query result is: a new array each render would re-run the
// sidebar's effects on every render.
const NO_MAIN_WORKTREES: never[] = [];

vi.mock("../../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({
    data: { chats, lastUserUpdateSequence: 0, mainWorktrees: NO_MAIN_WORKTREES },
    isLoading: false,
  }),
  useArchivedChats: () => ({ data: [], isFetched: true }),
  useDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn() }),
  useChat: () => ({ data: undefined }),
}));
// The Inbox nav item reads its own counts; nothing is waiting here.
vi.mock("../../../hooks/inbox-queries", async () => {
  const actual = await vi.importActual<typeof import("../../../hooks/inbox-queries")>(
    "../../../hooks/inbox-queries",
  );
  return {
    ...actual,
    useInboxCounts: () => ({ data: { blockingCount: 0, hasInformational: false }, isError: false }),
    useInboxProjectId: () => undefined,
  };
});
vi.mock("../../../hooks/message-queries", () => ({ useMarkUnread: () => ({ mutateAsync: vi.fn() }) }));

import { Sidebar } from "../Sidebar";
import { getCachedWorktrees } from "../../../hooks/worktree-queries";

describe("Sidebar and child runs", () => {
  beforeEach(() => {
    listRuns.mockClear();
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
    useChatStore.setState({ activeChatId: "chat-1", selectChat: vi.fn() } as Partial<
      ReturnType<typeof useChatStore.getState>
    >);
    // Empty on purpose: the sidebar groups from the chat list alone.
    useWorktreeStore.setState({
      currentWorktreeId: null,
      currentWorktreeProjectId: null,
      switchWorktreeContext: vi.fn(async () => undefined),
    } as Partial<ReturnType<typeof useWorktreeStore.getState>>);
    useProjectStore.setState({
      currentProject: {
        id: "project-1",
        name: "Project",
        path: "/tmp/project",
        is_git_repo: true,
        is_forge: false,
        created_at: "2024-01-01T00:00:00.000Z",
        updated_at: "2024-01-01T00:00:00.000Z",
        last_active: "2024-01-01T00:00:00.000Z",
      },
    } as Partial<ReturnType<typeof useProjectStore.getState>>);
  });

  it("groups chats under the workspace the list names, with no worktree in any store", async () => {
    // 113 workspaces exist in the project; the store holds none of them, and the
    // chat on workspace 112 is exactly the kind a paged worktree list dropped.
    const far = { ...workspace, id: "worktree-112", name: "Agent workspace 112", branch: "agent/112" };
    chats.push({ ...chats[0], id: "chat-far", title: "Chat far", worktreeId: far.id, worktree: far });
    try {
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      render(
        <QueryClientProvider client={queryClient}>
          <Sidebar />
        </QueryClientProvider>,
      );
      expect(await screen.findByText("Chat far")).toBeInTheDocument();
      // The group header shows the workspace's branch.
      expect(screen.getByText("agent/112")).toBeInTheDocument();
      expect(screen.getByText("feature/test")).toBeInTheDocument();
      expect(screen.queryByText("Unknown workspace")).not.toBeInTheDocument();
      expect(getCachedWorktrees()).toEqual([]);
    } finally {
      chats.pop();
    }
  });

  it("keeps a chat whose workspace row the server could not find, under a fallback group", async () => {
    chats.push({ ...chats[0], id: "chat-orphan", title: "Chat orphan", worktreeId: "worktree-gone", worktree: undefined });
    try {
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      render(
        <QueryClientProvider client={queryClient}>
          <Sidebar />
        </QueryClientProvider>,
      );
      expect(await screen.findByText("Chat orphan")).toBeInTheDocument();
      expect(screen.getByText("Unknown workspace")).toBeInTheDocument();
    } finally {
      chats.pop();
    }
  });

  it("renders every row without asking for any chat's child runs", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar />
      </QueryClientProvider>,
    );
    for (const chat of chats) expect(await screen.findByText(chat.title as string)).toBeInTheDocument();
    // Let any mounted queries fire.
    await new Promise((resolve) => setTimeout(resolve, 50));

    const childQueries = listRuns.mock.calls.filter(
      (call) => (call as unknown as [{ parentChatId?: string }])[0]?.parentChatId,
    );
    expect(childQueries).toEqual([]);
  });
});
