import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { Sidebar } from "./Sidebar";
import { ChatState, ChatActivity } from "../../gen/reliant/v1/chat_pb";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { useChatStore } from "../../store/chatStore";
import { useActivityStore } from "../../store/activityStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProcessStore } from "../../store/processStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatListPreferencesStore } from "../../store/chatListPreferencesStore";

vi.mock("../ui/Tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("../ui/Button", () => ({
  Button: ({ children, ...props }: ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button {...props}>{children}</button>
  ),
}));

vi.mock("../ui/ContextMenu", () => ({
  ContextMenu: () => null,
}));

// The real Dropdown is used here on purpose: these tests assert open/close
// behavior (choose-an-option and Escape), which a passthrough mock would fake.

vi.mock("../ui/ActivityDot", () => ({
  ActivityDot: () => <div data-testid="activity-dot" />,
}));

vi.mock("../../hooks/useDebounce", () => ({
  useDebounce: <T,>(value: T) => value,
}));

const mockChatListData = [
  {
    id: "chat-1",
    title: "Selected chat",
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    lastMessageAt: "2024-01-01T00:00:00.000Z",
    unread: false,
    state: ChatState.ACTIVE,
    worktreeId: "worktree-1",
    projectId: "project-1",
  },
];

const mockArchivedChatData: Array<Record<string, unknown>> = [];

vi.mock("../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: mockChatListData, isLoading: false }),
  useArchivedChats: () => ({ data: mockArchivedChatData, isFetched: true }),
  useDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("../../hooks/message-queries", () => ({
  useMarkUnread: () => ({ mutateAsync: vi.fn() }),
}));

describe("Sidebar selected chat scroll", () => {
  const fetchProcesses = vi.fn();
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
            state: ChatState.ACTIVE,
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

    useWorktreeStore.setState({
      worktrees: [
        {
          id: "worktree-1",
          name: "Workspace",
          path: "/tmp/worktree",
          branch: "feature/test",
          base_branch: "main",
          project_id: "project-1",
          status: WorktreeStatus.ACTIVE,
          is_main: false,
          created_at: "2024-01-01T00:00:00.000Z",
          updated_at: "2024-01-01T00:00:00.000Z",
          last_active: "2024-01-01T00:00:00.000Z",
        },
      ],
      currentWorktree: null,
      switchWorktreeContext,
    } as Partial<ReturnType<typeof useWorktreeStore.getState>>);

    useProcessStore.setState({
      fetchProcesses,
    } as Partial<ReturnType<typeof useProcessStore.getState>>);

    useProjectStore.setState({
      currentProject: {
        id: "project-1",
        name: "Project",
        path: "/tmp/project",
        is_git_repo: true,
        worktree_count: 1,
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
    const onNavigateToProjectPicker = vi.fn();
    const onOpenWorkflows = vi.fn();
    const onOpenChatSearch = vi.fn();
    const onNavigateToSettings = vi.fn();

    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar
          onNavigateToProjectPicker={onNavigateToProjectPicker}
          onOpenWorkflows={onOpenWorkflows}
          onOpenChatSearch={onOpenChatSearch}
          onNavigateToSettings={onNavigateToSettings}
        />
      </QueryClientProvider>
    );

    expect(screen.getByRole("button", { name: "New chat" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Projects" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Workflows" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Search" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Settings" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Workflows" })).toHaveAttribute(
      "data-onboarding",
      "workflow-button"
    );

    fireEvent.click(screen.getByRole("button", { name: "Projects" }));
    fireEvent.click(screen.getByRole("button", { name: "Workflows" }));
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));

    expect(onNavigateToProjectPicker).toHaveBeenCalledTimes(1);
    expect(onOpenWorkflows).toHaveBeenCalledTimes(1);
    expect(onOpenChatSearch).toHaveBeenCalledTimes(1);
    expect(onNavigateToSettings).toHaveBeenCalledTimes(1);
  });
});
