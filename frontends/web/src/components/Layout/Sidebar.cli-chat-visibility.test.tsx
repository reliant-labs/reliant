/**
 * Regression: a chat started with no worktree_id must appear in the sidebar.
 * (A `reliant workflow run` start is now a cli.run run, listed in Runs until
 * adopted; once adopted it lists here like any chat, so the grouping below
 * still applies to it.)
 *
 * The CLI sends no worktree_id, and chats used to persist with worktree_id
 * NULL. The sidebar groups by worktree and drops chats whose worktree does not
 * resolve, so CLI runs executed correctly but were invisible.
 *
 * The fix makes the null state unrepresentable: StartChat resolves an omitted
 * worktree_id to the project's main worktree and refuses to persist a chat
 * without one (see resolveChatWorktreeID and
 * internal/grpc/services/chat_worktree_invariant_test.go). This test guards the
 * rendering half of that contract — a CLI chat carrying the main worktree id
 * lands in the main workspace group alongside UI-created chats.
 */
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { Sidebar } from "./Sidebar";
import { ChatState, ChatActivity } from "../../gen/reliant/v1/chat_pb";
import { useChatStore } from "../../store/chatStore";
import { useActivityStore } from "../../store/activityStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatListPreferencesStore } from "../../store/chatListPreferencesStore";

vi.mock("../forge-ui/tooltip", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("../forge-ui/context_menu", () => ({ default: () => null }));

vi.mock("../../hooks/useDebounce", () => ({
  useDebounce: <T,>(value: T) => value,
}));

const MAIN_WORKTREE_ID = "worktree-main";

// Both chats carry the main worktree: the UI names it explicitly, and the
// server resolves the CLI's omitted worktree_id to the same row.
const mockChatListData: Array<Record<string, unknown>> = [
  {
    id: "chat-ui",
    title: "Chat from the UI",
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    lastMessageAt: "2024-01-01T00:00:00.000Z",
    unread: false,
    state: ChatState.IDLE,
    worktreeId: MAIN_WORKTREE_ID,
    projectId: "project-1",
  },
  {
    id: "chat-cli",
    title: "Chat from the CLI",
    createdAt: "2024-01-02T00:00:00.000Z",
    updatedAt: "2024-01-02T00:00:00.000Z",
    lastMessageAt: "2024-01-02T00:00:00.000Z",
    unread: false,
    state: ChatState.IDLE,
    worktreeId: MAIN_WORKTREE_ID,
    projectId: "project-1",
  },
];

vi.mock("../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({
    data: {
      chats: mockChatListData,
      lastUserUpdateSequence: 0,
      mainWorktrees: [
        {
          id: MAIN_WORKTREE_ID,
          name: "main",
          branch: "main",
          path: "/tmp/project",
          isMain: true,
          status: 1,
          projectId: "project-1",
        },
      ],
    },
    isLoading: false,
  }),
  useArchivedChats: () => ({ data: [], isFetched: true }),
  useDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn() }),
  useChat: () => ({ data: undefined }),
}));

// The Inbox nav item reads its own counts; nothing is waiting here.
vi.mock("../../hooks/inbox-queries", async () => {
  const actual = await vi.importActual<typeof import("../../hooks/inbox-queries")>(
    "../../hooks/inbox-queries",
  );
  return {
    ...actual,
    useInboxCounts: () => ({ data: { blockingCount: 0, hasInformational: false }, isError: false }),
    useInboxProjectId: () => undefined,
  };
});

// The automation pill polls live runs; there are none here.
vi.mock("../../hooks/run-queries", async () => {
  const actual = await vi.importActual<typeof import("../../hooks/run-queries")>(
    "../../hooks/run-queries",
  );
  return { ...actual, useLiveRuns: () => ({ data: undefined }) };
});

vi.mock("../../hooks/message-queries", () => ({
  useMarkUnread: () => ({ mutateAsync: vi.fn() }),
}));

function renderSidebar() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <Sidebar />
    </QueryClientProvider>
  );
}

describe("Sidebar visibility for CLI-launched chats", () => {
  beforeEach(() => {
    vi.clearAllMocks();

    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    });

    useChatStore.setState({
      chats: new Map(),
      activeChatId: null,
      selectChat: vi.fn(),
    } as Partial<ReturnType<typeof useChatStore.getState>>);

    useActivityStore.setState({
      activities: new Map([
        ["chat-ui", ChatActivity.IDLE],
        ["chat-cli", ChatActivity.IDLE],
      ]),
    } as Partial<ReturnType<typeof useActivityStore.getState>>);

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

    useChatListPreferencesStore.setState({
      sortOrder: "recent_activity",
      viewMode: "grouped",
      filters: {},
      setSortOrder: vi.fn(),
      setViewMode: vi.fn(),
      setFilters: vi.fn(),
      resetFilters: vi.fn(),
      resetAll: vi.fn(),
    });
  });

  it("shows a CLI-created chat bound to the main worktree (grouped view)", () => {
    renderSidebar();

    expect(screen.getByText("Chat from the UI")).toBeInTheDocument();
    expect(screen.getByText("Chat from the CLI")).toBeInTheDocument();
  });

  it("shows a CLI-created chat bound to the main worktree (flat view)", () => {
    useChatListPreferencesStore.setState({ viewMode: "flat" });

    renderSidebar();

    expect(screen.getByText("Chat from the UI")).toBeInTheDocument();
    expect(screen.getByText("Chat from the CLI")).toBeInTheDocument();
  });
});
