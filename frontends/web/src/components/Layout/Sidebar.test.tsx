import { fireEvent, render, screen, within } from "@testing-library/react";
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
import { beforeEach as shellBeforeEach } from "vitest";
import { ALL_PROJECTS_PICK, useShellLastPick } from "./sidebar/shellProject";

// The project shell (U6b): these tests read the list across every project, as "All projects" shows it, on the chat home where Chats are open.
shellBeforeEach(() => useShellLastPick.setState({ key: ALL_PROJECTS_PICK }));

// Keeps the trigger wrapper's classes (triggerClassName): the hover-only
// actions hide and show on it.
// The machine filter's daemon list: no machines, so the filter is hidden.
vi.mock("../../hooks/useOnboardingQueries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/useOnboardingQueries")>()),
  useDaemonList: () => NO_MACHINES,
}));
const NO_MACHINES = { data: [] };
vi.mock("../forge-ui/tooltip", () => ({
  default: ({ children, triggerClassName }: { children: ReactNode; triggerClassName?: string }) =>
    triggerClassName ? <span className={triggerClassName}>{children}</span> : <>{children}</>,
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
// The archived read's paging state, and the chat detail useChat answers.
const mockArchivedPaging = { hasNextPage: false, isFetchingNextPage: false, fetchNextPage: vi.fn() };
const mockUseArchivedChats = vi.fn((_options?: { enabled?: boolean }) => ({
  data: mockArchivedChatData,
  isFetched: true,
  ...mockArchivedPaging,
}));
let mockOpenedChat: Record<string, unknown> | undefined;

// Stable, as a query result is: a new array each render would re-run the
// sidebar's effects on every render.
const NO_MAIN_WORKTREES: never[] = [];

vi.mock("../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({
    data: { chats: mockChatListData, lastUserUpdateSequence: 0, mainWorktrees: NO_MAIN_WORKTREES },
    isLoading: false,
  }),
  useArchivedChats: (options?: { enabled?: boolean }) => mockUseArchivedChats(options),
  usePermanentlyDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn(), mutate: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn() }),
  useChat: () => ({ data: mockOpenedChat }),
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
    mockArchivedPaging.hasNextPage = false;
    mockOpenedChat = undefined;

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
        <Sidebar pathname="/" />
      </QueryClientProvider>
    );

    vi.advanceTimersByTime(100);

    expect(scrollIntoViewMock).toHaveBeenCalledWith({
      block: "nearest",
      behavior: "auto",
    });

    vi.useRealTimers();
  });

  it("closes the view menu after choosing a view mode", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar pathname="/" />
      </QueryClientProvider>
    );

    fireEvent.click(screen.getByTestId("project-filter-trigger"));
    fireEvent.click(screen.getByText("Flat list"));
    expect(screen.queryByText("Flat list")).not.toBeInTheDocument();
  });

  // At the default 240px width a workspace's name read "percent-r…": its
  // hover-only buttons were opacity-0 and still took their width (#624).
  it("keeps hover-only actions out of the layout, and names truncated titles in full", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar pathname="/" />
      </QueryClientProvider>
    );

    // A truncated name is still readable in full, on hover, with when it
    // last changed: the row has no time column.
    expect(screen.getByText("Selected chat").getAttribute("title")).toMatch(/^Selected chat\nUpdated .+ ago$/);
    const workspaceTitle = screen.getByText("feature/test");
    expect(workspaceTitle).toHaveAttribute("title", "feature/test");

    // The section heading's New chat and Archive (and, on a project's first
    // heading, New chat in the project): display:none until the heading is
    // hovered or focused, never a transparent box holding width.
    const header = workspaceTitle.closest(".group\\/ws") as HTMLElement;
    const hoverOnly = Array.from(header.querySelectorAll("button")).filter(
      (b) => !b.contains(workspaceTitle) && b.querySelector(".lucide-plus, .lucide-archive"),
    );
    expect(hoverOnly.length).toBeGreaterThanOrEqual(2);
    for (const button of hoverOnly) {
      expect(button.className).not.toMatch(/opacity-0/);
      const wrapper = button.parentElement!;
      expect(wrapper.className).toMatch(/(^|\s)hidden(\s|$)/);
      expect(wrapper.className).toContain("group-hover/ws:flex");
      expect(wrapper.className).toContain("group-focus-within/ws:flex");
    }

    // The chat row's More options likewise, and it is the row's only hover
    // control: Archive is in the menu it opens, not one click away.
    const row = screen.getByText("Selected chat").closest("[data-chat-id]") as HTMLElement;
    const more = row.querySelector('button[aria-haspopup="menu"]')!.closest("div")!;
    expect(more.className).toMatch(/(^|\s)hidden(\s|$)/);
    expect(more.className).toContain("group-hover/chat:flex");
    expect(row.querySelector(".lucide-archive")).toBeNull();
  });

  it("draws the default nav (B): the top level off an area, the footer Inbox, Usage & billing, Settings, each through onNavigate", () => {
    sessionStorage.clear();
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const onNavigate = vi.fn();

    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar pathname="/inbox" onNavigate={onNavigate} />
      </QueryClientProvider>
    );

    // Find first, then the switcher, then the nav (U6c).
    expect(screen.getByTestId("nav-find")).toBeInTheDocument();
    const nav = screen.getByTestId("nav-top");
    expect(screen.getByTestId("sidebar-home-button")).toHaveTextContent("Overview");
    expect(within(nav).getByTestId("nav-agents")).toHaveTextContent("Agents");
    // No chat list off an Agents route: it lives in the Agents drill-in.
    expect(screen.queryByTestId("sidebar-chats")).toBeNull();
    expect(screen.getByTestId("inbox-badge-count")).toHaveTextContent("2");
    expect(screen.getByTestId("sidebar-inbox-button")).toHaveAttribute("aria-current", "page");

    fireEvent.click(within(nav).getByTestId("nav-agents"));
    fireEvent.click(screen.getByTestId("nav-usage-billing"));
    fireEvent.click(screen.getByRole("link", { name: "Settings" }));
    expect(onNavigate.mock.calls.map(([href]) => href)).toEqual(["/", "/usage", "/settings"]);
  });

  it("on a chat page, drills into Agents: New chat, Workflows, Sandboxes, Connectors, then the chats full height", () => {
    sessionStorage.clear();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onNavigate = vi.fn();
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar pathname="/" onNavigate={onNavigate} />
      </QueryClientProvider>
    );
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "agents");
    expect(screen.queryByTestId("nav-top")).toBeNull();
    expect(screen.getByTestId("create-chat-button")).toHaveAttribute("href", "/new");
    expect(screen.getByTestId("nav-workflows")).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByTestId("nav-sandboxes")).toBeInTheDocument();
    // AI providers lives in Settings only (U6e).
    expect(screen.queryByTestId("nav-ai-providers")).toBeNull();
    expect(screen.getByTestId("sidebar-chats")).toHaveAttribute("data-place", "fill");
    fireEvent.click(screen.getByTestId("nav-back"));
    expect(onNavigate).toHaveBeenCalledTimes(1);
  });

  it("leaves a modified click on a row to the browser (a new tab)", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onNavigate = vi.fn();
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar pathname="/" onNavigate={onNavigate} />
      </QueryClientProvider>
    );
    fireEvent.click(screen.getByRole("link", { name: "Settings" }), { metaKey: true });
    expect(onNavigate).not.toHaveBeenCalled();
  });

  it("on a settings page, Settings replaces the nav with its sections", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar pathname="/settings/appearance" onNavigate={vi.fn()} />
      </QueryClientProvider>
    );
    expect(screen.getByTestId("nav-drill-in")).toHaveAttribute("data-area", "settings");
    expect(screen.getByTestId("nav-settings-appearance")).toHaveAttribute("aria-current", "page");
    expect(screen.queryByTestId("sidebar-chats")).toBeNull();
  });

  it("links Home to the summary of every project, current only there", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar pathname="/projects" onNavigate={vi.fn()} />
      </QueryClientProvider>
    );
    const home = screen.getByTestId("sidebar-home-button");
    expect(home).toHaveAttribute("href", "/projects");
    expect(home).toHaveAttribute("aria-current", "page");
  });

  it("marks Settings while a settings page is on screen", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar pathname="/settings/appearance" onNavigate={vi.fn()} />
      </QueryClientProvider>
    );
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute("aria-current", "page");
  });
});
