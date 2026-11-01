import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { Sidebar } from "./Sidebar";
import { ChatState, ChatActivity } from "../../gen/reliant/v1/chat_pb";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { useChatStore } from "../../store/chatStore";
import { useActivityStore } from "../../store/activityStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatListPreferencesStore } from "../../store/chatListPreferencesStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";

vi.mock("../forge-ui/tooltip", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("../forge-ui/context_menu", () => ({ default: () => null }));

// The real Dropdown is used here on purpose: these tests assert open/close
// behavior (choose-an-option and Escape), which a passthrough mock would fake.

vi.mock("../../hooks/useDebounce", () => ({
  useDebounce: <T,>(value: T) => value,
}));

// What the server sends: the chat carries its workspace, so none of this needs
// the worktree store.
const featureWorktree = {
  id: "worktree-1",
  name: "Workspace",
  branch: "feature/test",
  path: "/tmp/worktree",
  isMain: false,
  status: WorktreeStatus.ACTIVE,
};

const mockChatListData = [
  {
    id: "chat-1",
    title: "Selected chat",
    worktree: featureWorktree,
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    lastMessageAt: "2024-01-01T00:00:00.000Z",
    unread: false,
    state: ChatState.IDLE,
    worktreeId: "worktree-1",
    projectId: "project-1",
  },
];

const mockArchivedChatData: Array<Record<string, unknown>> = [];

// Stable, as a query result is: a new array each render would re-run the
// sidebar's effects on every render.
const NO_MAIN_WORKTREES: never[] = [];

vi.mock("../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({
    data: { chats: mockChatListData, lastUserUpdateSequence: 0, mainWorktrees: NO_MAIN_WORKTREES },
    isLoading: false,
  }),
  useArchivedChats: () => ({ data: mockArchivedChatData, isFetched: true }),
  useDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn() }),
  useChat: () => ({ data: undefined }),
}));

// The Inbox nav item reads its own counts; the Sidebar test only checks it is
// placed and wired.
vi.mock("../../hooks/inbox-queries", () => ({
  useInboxCounts: () => ({ data: { blockingCount: 2, hasInformational: false }, isError: false }),
  useInboxProjectId: () => undefined,
}));

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

describe("Sidebar selected chat scroll", () => {
  const switchWorktreeContext = vi.fn(async () => undefined);
  const scrollIntoViewMock = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    scrollIntoViewMock.mockReset();
    mockArchivedChatData.length = 0;

    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: scrollIntoViewMock,
    });

    useChatStore.setState({
      chats: new Map([
        [
          "chat-1",
          {
            id: "chat-1",
            title: "Selected chat",
            createdAt: "2024-01-01T00:00:00.000Z",
            updatedAt: "2024-01-01T00:00:00.000Z",
            lastMessageAt: "2024-01-01T00:00:00.000Z",
            unread: false,
            state: ChatState.IDLE,
            worktreeId: "worktree-1",
            projectId: "project-1",
          },
        ],
      ]),
      activeChatId: "chat-1",
      selectChat: vi.fn(),
    } as Partial<ReturnType<typeof useChatStore.getState>>);

    useActivityStore.setState({
      activities: new Map([["chat-1", ChatActivity.IDLE]]),
    } as Partial<ReturnType<typeof useActivityStore.getState>>);

    // Empty on purpose: the sidebar groups from the chat list alone.
    useWorktreeStore.setState({
      currentWorktreeId: null,
      currentWorktreeProjectId: null,
      switchWorktreeContext,
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

  it("scrolls the selected chat into view with auto behavior", async () => {
    vi.useFakeTimers();

    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar />
      </QueryClientProvider>
    );

    vi.advanceTimersByTime(100);

    expect(scrollIntoViewMock).toHaveBeenCalledWith({
      block: "nearest",
      behavior: "auto",
    });

    vi.useRealTimers();
  });

  it("stays on the archived tab when expanding an archived workspace group", () => {
    mockArchivedChatData.push({
      id: "chat-archived",
      title: "Archived chat",
      createdAt: "2024-01-01T00:00:00.000Z",
      updatedAt: "2024-01-01T00:00:00.000Z",
      lastMessageAt: "2024-01-01T00:00:00.000Z",
      unread: false,
      state: ChatState.ARCHIVED,
      worktreeId: "worktree-archived",
      worktreeName: "Archived workspace",
      projectId: "project-1",
    });
    // One project's archive groups by workspace (across projects it groups
    // by project: Sidebar.projectClarity.test.tsx).
    useWorkspaceStateStore.setState({ projectFilterId: "project-1" });

    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar />
      </QueryClientProvider>
    );

    // The active chat is a non-archived one, so the sidebar starts on "Active".
    fireEvent.click(screen.getByLabelText("Sort chats"));
    fireEvent.click(screen.getByText("Archived chats"));

    const groupHeader = screen.getByText("Archived workspace");
    expect(screen.queryByText("Archived chat")).not.toBeInTheDocument();

    fireEvent.click(groupHeader);

    // The group expands and the list must NOT snap back to the active tab.
    expect(screen.getByText("Archived chat")).toBeInTheDocument();
    expect(screen.queryByText("Selected chat")).not.toBeInTheDocument();
    useWorkspaceStateStore.setState({ projectFilterId: null });
  });

  it("closes the list menu after choosing an option, and on Escape", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar />
      </QueryClientProvider>
    );

    // Picking an option is a complete interaction — the menu should dismiss.
    fireEvent.click(screen.getByLabelText("Sort chats"));
    fireEvent.click(screen.getByText("Archived chats"));
    expect(screen.queryByText("Archived chats")).not.toBeInTheDocument();

    // Sort options dismiss too.
    fireEvent.click(screen.getByLabelText("Sort chats"));
    fireEvent.click(screen.getByText("Newest First"));
    expect(screen.queryByText("Newest First")).not.toBeInTheDocument();

    // Escape still dismisses without choosing anything.
    fireEvent.click(screen.getByLabelText("Sort chats"));
    expect(screen.getByText("Archived chats")).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByText("Archived chats")).not.toBeInTheDocument();
  });

  it("closes the view menu after choosing a view mode", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar />
      </QueryClientProvider>
    );

    fireEvent.click(screen.getByLabelText("View options"));
    fireEvent.click(screen.getByText("Flat list"));
    expect(screen.queryByText("Flat list")).not.toBeInTheDocument();
  });

  it("renders Codex-style navigation actions and calls their handlers", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const onOpenWorkflows = vi.fn();
    const onOpenChatSearch = vi.fn();
    const onNavigateToSettings = vi.fn();
    const onOpenInbox = vi.fn();

    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar
          onOpenWorkflows={onOpenWorkflows}
          onOpenChatSearch={onOpenChatSearch}
          onNavigateToSettings={onNavigateToSettings}
          onOpenInbox={onOpenInbox}
        />
      </QueryClientProvider>
    );

    expect(screen.getByRole("button", { name: "New chat" })).toBeInTheDocument();
    // Inbox is in the nav with its badge; its position (after Apps) is pinned in Sidebar.forge-nav.test.tsx.
    const nav = screen.getByRole("navigation", { name: "Chat sidebar navigation" });
    expect(nav).toContainElement(screen.getByTestId("sidebar-inbox-button"));
    expect(screen.getByTestId("inbox-badge-count")).toHaveTextContent("2");
    // No "Projects" page to go to: the project is the chat list's filter.
    expect(screen.queryByRole("button", { name: "Projects" })).toBeNull();
    expect(screen.getByTestId("project-filter-label")).toHaveTextContent("All projects");
    expect(screen.getByRole("button", { name: "Workflows" })).toBeInTheDocument();
    // Automations and Runs are tabs of the Workflows area now (§1.2), not
    // sidebar entries of their own.
    expect(screen.queryByRole("button", { name: "Automations" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Runs" })).toBeNull();
    expect(screen.getByRole("button", { name: "Search" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Settings" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Workflows" })).toHaveAttribute(
      "data-onboarding",
      "workflow-button"
    );

    fireEvent.click(screen.getByRole("button", { name: "Workflows" }));
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    fireEvent.click(screen.getByTestId("sidebar-inbox-button"));

    expect(onOpenInbox).toHaveBeenCalledTimes(1);
    expect(onOpenWorkflows).toHaveBeenCalledTimes(1);
    expect(onOpenChatSearch).toHaveBeenCalledTimes(1);
    expect(onNavigateToSettings).toHaveBeenCalledTimes(1);
  });
});
