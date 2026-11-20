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

// Stable, as a query result is: a new array each render would re-run the
// sidebar's effects on every render.
const NO_MAIN_WORKTREES: never[] = [];

vi.mock("../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({
    data: { chats: mockChatListData, lastUserUpdateSequence: 0, mainWorktrees: NO_MAIN_WORKTREES },
    isLoading: false,
  }),
  useArchivedChats: () => ({ data: mockArchivedChatData, isFetched: true }),
  usePermanentlyDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn(), mutate: vi.fn() }),
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

    // The workspace header's New chat and Archive: display:none until the
    // header is hovered or focused, never a transparent box holding width.
    const header = workspaceTitle.closest(".group\\/ws") as HTMLElement;
    const hoverOnly = Array.from(header.querySelectorAll("button")).filter(
      (b) => !b.contains(workspaceTitle) && b.querySelector(".lucide-plus, .lucide-archive"),
    );
    expect(hoverOnly).toHaveLength(2);
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

  it("orders the project shell's nav Chats, Inbox, Search, Workflows, and opens each page through onNavigate", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const onNavigate = vi.fn();
    const onOpenChatSearch = vi.fn();

    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar pathname="/" onNavigate={onNavigate} onOpenChatSearch={onOpenChatSearch} />
      </QueryClientProvider>
    );

    // The project shell (U6b): Chats (open on the chat home, New chat first),
    // Inbox, Search, Workflows; no Projects, Usage or Apps rows.
    const nav = screen.getByRole("navigation", { name: "Chat sidebar navigation" });
    const order = [...nav.querySelectorAll("a, button")]
      .map((el) => el.getAttribute("aria-label") ?? el.textContent?.trim() ?? "")
      .filter((name) => ["Chats", "New chat", "Inbox", "Search", "Workflows"].some((label) => name.startsWith(label)));
    expect(order.slice(0, 2).map((name) => name.replace(/Ctrl.*$/, ""))).toEqual(["Chats", "New chat"]);
    expect(order.filter((name) => ["Inbox", "Search", "Workflows"].some((label) => name.startsWith(label))).map((name) => name.match(/^(Inbox|Search|Workflows)/)?.[1])).toEqual([
      "Inbox",
      "Search",
      "Workflows",
    ]);
    expect(within(nav).queryByRole("link", { name: "Projects" })).toBeNull();
    expect(within(nav).queryByRole("link", { name: "Usage" })).toBeNull();
    expect(screen.getByTestId("create-chat-button")).toHaveAttribute("href", "/new");
    expect(screen.getByTestId("sidebar-chats-toggle")).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByTestId("inbox-badge-count")).toHaveTextContent("2");
    const workflows = within(nav).getByRole("link", { name: "Workflows" });
    // Automations and Runs are tabs of the Workflows area (§1.2): nested
    // under Workflows only while the area is open, never places of their own.
    expect(screen.queryByRole("link", { name: "Automations" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Runs" })).toBeNull();
    const settings = screen.getByRole("link", { name: "Settings" });
    expect(workflows).toHaveAttribute("data-onboarding", "workflow-button");

    fireEvent.click(workflows);
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(settings);
    fireEvent.click(screen.getByTestId("sidebar-inbox-button"));

    expect(onNavigate.mock.calls.map(([href]) => href)).toEqual(["/workflows", "/settings", "/inbox"]);
    expect(onOpenChatSearch).toHaveBeenCalledTimes(1);
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

  it("folds Chats off a chat page, and its chevron opens them there without navigating", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onNavigate = vi.fn();
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar pathname="/settings/appearance" onNavigate={onNavigate} />
      </QueryClientProvider>
    );
    const toggle = screen.getByTestId("sidebar-chats-toggle");
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("sidebar-chats")).toBeNull();
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByTestId("sidebar-chats")).toBeInTheDocument();
    expect(onNavigate).not.toHaveBeenCalled();
    // The Chats row itself is a real link to the chats.
    expect(screen.getByTestId("sidebar-chats-button").tagName).toBe("A");
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
