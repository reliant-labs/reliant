/**
 * The mobile chat list — grouping by workspace, main-first group ordering,
 * and attention-first ordering within a group.
 *
 * `GroupedVirtuoso` measures the viewport via ResizeObserver, which jsdom
 * doesn't implement, so by default it renders zero rows. `VirtuosoMockContext`
 * is react-virtuoso's own escape hatch for exactly this — it fixes a
 * viewport/item height so every row renders unconditionally, which is also
 * what makes asserting on row order meaningful without a real scroll.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { VirtuosoMockContext } from "react-virtuoso";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ChatActivity, ChatState } from "../../../gen/reliant/v1/chat_pb";
import { getCachedWorktrees } from "../../../hooks/worktree-queries";

const navigate = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
  useNavigate: () => navigate,
  useRouterState: () => "/m/chats",
}));

// The list response: chats (each carrying its workspace) plus the project's
// main workspace. No workspace read is consulted, so the cache holds none — a
// workspace that is in no cache must still group correctly.
const useChatListWithWorkspaces = vi.fn();
vi.mock("../../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: (...args: unknown[]) => useChatListWithWorkspaces(...args),
  // Real key factory, not a stub: the archive flow invalidates
  // `chatKeys.lists()` to clear chats whose worktree just went away, and a
  // mismatched key would make the assertion pass while the app still showed a
  // ghost "Unknown workspace" group.
  chatKeys: {
    all: ["chats"] as const,
    lists: () => ["chats", "list"] as const,
  },
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject: { id: "p1", name: "reliant" } }),
}));

const archiveWorktree = vi.hoisted(() => vi.fn());

vi.mock("../../../hooks/worktree-actions", () => ({
  archiveWorktree: (...args: unknown[]) => archiveWorktree(...args),
}));

type Workspace = {
  id: string;
  name: string;
  branch: string;
  isMain: boolean;
  deletedAt?: string;
};
const MAIN: Workspace = { id: "wt-main", name: "reliant", branch: "main", isMain: true };
const FEATURE: Workspace = { id: "wt-feature", name: "feature-x", branch: "feature/x", isMain: false };
const WORKSPACES: Record<string, Workspace> = { [MAIN.id]: MAIN, [FEATURE.id]: FEATURE };

/** The list response for these chats, joined the way the server joins it. */
function listOf(chats: Array<ReturnType<typeof chat>>, mainWorktree: Workspace | undefined = MAIN) {
  return {
    data: {
      chats: chats.map((c) => ({ ...c, worktree: WORKSPACES[c.worktreeId] })),
      lastUserUpdateSequence: 0,
      mainWorktrees: mainWorktree ? [{ ...mainWorktree, projectId: "p1" }] : [],
    },
    isLoading: false,
  };
}

vi.mock("../../../store/mobileDrawerStore", () => ({
  useMobileDrawerStore: (selector: (s: unknown) => unknown) =>
    selector({ isOpen: false, open: vi.fn(), close: vi.fn(), inboxBadge: { count: 0, informational: false, degraded: false, announcement: "" } }),
}));

// The sheet has its own tests; here it only needs to report a creation.
vi.mock("../MobileCreateWorkspaceSheet", () => ({
  MobileCreateWorkspaceSheet: ({
    onCreated,
  }: {
    onCreated: (worktree: { id: string }) => void;
  }) => (
    <div role="dialog" aria-label="New workspace">
      <button type="button" onClick={() => onCreated({ id: "wt-new" })}>
        Create stub
      </button>
    </div>
  ),
}));

vi.mock("../MobileWorkspaceSheet", () => ({
  MobileWorkspaceSheet: ({
    worktreeId,
    chatId,
    title,
  }: {
    worktreeId?: string;
    chatId?: string;
    title?: string;
  }) => (
    <div role="dialog" aria-label="Workspace panels">
      {title}:{worktreeId}:{chatId ?? "no-chat"}
    </div>
  ),
}));

const { MobileChatList, buildGroups } = await import("../MobileChatList");
const { useWorkspaceStateStore } = await import("../../../store/workspaceStateStore");
const { SurfaceProvider } = await import("../../../lib/surfaceContext");

function renderList() {
  // The component invalidates the chat list after archiving a worktree, so it
  // calls `useQueryClient` on every render — without a provider that throws
  // before anything renders, and every test in this file fails identically.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <SurfaceProvider surface="mobile">
      <QueryClientProvider client={client}>
        <VirtuosoMockContext.Provider value={{ viewportHeight: 1000, itemHeight: 64 }}>
          <MobileChatList />
        </VirtuosoMockContext.Provider>
      </QueryClientProvider>
    </SurfaceProvider>,
  );
}

function chat(overrides: Partial<{
  id: string;
  title: string;
  worktreeId: string;
  activity: ChatActivity;
  workingThreads: number;
  needsRecovery: boolean;
  unread: boolean;
  lastMessageAt: string;
  state: ChatState;
}>) {
  return {
    id: "chat-default",
    projectId: "p1",
    title: "Untitled",
    worktreeId: "wt-main",
    activity: ChatActivity.IDLE,
    workingThreads: 0,
    needsRecovery: false,
    unread: false,
    lastMessageAt: "2024-01-01T00:00:00.000Z",
    state: ChatState.IDLE,
    ...overrides,
  };
}

beforeEach(() => {
  // These tests are about one project's list (the /m/projects filter on p1);
  // the all-projects grouping is pinned in its own describe below.
  useWorkspaceStateStore.setState({ projectFilterId: "p1" });
  navigate.mockReset();
  archiveWorktree.mockReset();
  useChatListWithWorkspaces.mockReset();
});

describe("MobileChatList", () => {
  it("groups chats by worktree with the main workspace first", () => {
    useChatListWithWorkspaces.mockReturnValue(listOf([
        chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature", lastMessageAt: "2024-01-03T00:00:00.000Z" }),
        chat({ id: "c2", title: "Main chat", worktreeId: "wt-main", lastMessageAt: "2024-01-01T00:00:00.000Z" }),
      ]));

    renderList();

    const groupHeadings = screen.getAllByText(/reliant|feature-x/);
    // "reliant" (main) is listed first even though "feature-x" has the more
    // recent chat — main always sorts first regardless of activity.
    expect(groupHeadings[0]).toHaveTextContent("reliant");
    expect(screen.getByText("Main chat")).toBeInTheDocument();
    expect(screen.getByText("Branch chat")).toBeInTheDocument();
  });

  it("names every group from the list response with no worktree in the store", () => {
    // 113 workspaces exist; the store holds none, and these two chats sit on
    // workspaces a paged worktree list would have dropped.
    const far = { id: "wt-112", name: "agent-112", branch: "agent/112", isMain: false };
    useChatListWithWorkspaces.mockReturnValue({
      data: {
        chats: [
          { ...chat({ id: "c1", title: "Far chat", worktreeId: "wt-112" }), worktree: far },
          { ...chat({ id: "c2", title: "Main chat", worktreeId: "wt-main" }), worktree: MAIN },
        ],
        lastUserUpdateSequence: 0,
        mainWorktrees: [{ ...MAIN, projectId: "p1" }],
      },
      isLoading: false,
    });
    renderList();

    expect(getCachedWorktrees()).toEqual([]);
    expect(screen.getByText("agent-112")).toBeInTheDocument();
    expect(screen.getByText("Far chat")).toBeInTheDocument();
    expect(screen.queryByText("Unknown workspace")).not.toBeInTheDocument();
  });

  it("puts a chat with no worktree under the main workspace", () => {
    useChatListWithWorkspaces.mockReturnValue({
      data: {
        chats: [{ ...chat({ id: "c1", title: "Unbound chat" }), worktreeId: undefined, worktree: undefined }],
        lastUserUpdateSequence: 0,
        mainWorktrees: [{ ...MAIN, projectId: "p1" }],
      },
      isLoading: false,
    });
    renderList();

    expect(screen.getByText("Unbound chat")).toBeInTheDocument();
    expect(screen.getByText("reliant")).toBeInTheDocument();
    expect(screen.queryByText("Unknown workspace")).not.toBeInTheDocument();
  });

  it("shows the main workspace even with zero chats", () => {
    useChatListWithWorkspaces.mockReturnValue(listOf([]));
    renderList();
    expect(screen.getByText("reliant")).toBeInTheDocument();
    expect(screen.getByText("0", { selector: "span" })).toBeInTheDocument();
  });

  it("sorts a needing-attention group before a merely-recent group", () => {
    useChatListWithWorkspaces.mockReturnValue(listOf([
        chat({ id: "c1", title: "Recent branch chat", worktreeId: "wt-feature", lastMessageAt: "2024-01-05T00:00:00.000Z" }),
        chat({
          id: "c2",
          title: "Stuck main chat",
          worktreeId: "wt-main",
          activity: ChatActivity.AWAITING_INPUT,
          lastMessageAt: "2024-01-01T00:00:00.000Z",
        }),
      ]));

    renderList();
    // Main is pinned first by the isMain rule regardless of activity, so this
    // pins that rule rather than re-deriving activity-vs-recency ordering
    // (covered separately by the within-group test below).
    const headings = screen.getAllByText(/reliant|feature-x/);
    expect(headings[0]).toHaveTextContent("reliant");
  });

  it("orders chats within a group by attention first, then recency", () => {
    useChatListWithWorkspaces.mockReturnValue(listOf([
        chat({ id: "c1", title: "Older, needs attention", worktreeId: "wt-main", activity: ChatActivity.AWAITING_INPUT, lastMessageAt: "2024-01-01T00:00:00.000Z" }),
        chat({ id: "c2", title: "Newer, idle", worktreeId: "wt-main", lastMessageAt: "2024-01-05T00:00:00.000Z" }),
      ]));

    renderList();
    const rows = screen.getAllByText(/Older, needs attention|Newer, idle/);
    expect(rows[0]).toHaveTextContent("Older, needs attention");
    expect(rows[1]).toHaveTextContent("Newer, idle");
    expect(screen.getByText("Needs your input")).toBeInTheDocument();
  });

  it("shows needs-input apart from unread and failure, with the agents still running", () => {
    useChatListWithWorkspaces.mockReturnValue(listOf([
        chat({ id: "c1", title: "Asking", activity: ChatActivity.AWAITING_INPUT, workingThreads: 2 }),
        chat({ id: "c2", title: "Failed", activity: ChatActivity.ERROR, unread: true }),
      ]));

    renderList();
    expect(screen.getByTestId("mobile-chat-needs-input")).toHaveTextContent("Needs your input");
    expect(screen.getByTestId("mobile-chat-still-running")).toHaveAccessibleName("2 agents still running");
    // A failure keeps its own words, and its unread dot is no longer hidden.
    expect(screen.getByText("Needs you")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Unread" })).toBeInTheDocument();
  });

  it("collapses and expands a group", async () => {
    const user = userEvent.setup();
    useChatListWithWorkspaces.mockReturnValue(listOf([chat({ id: "c1", title: "Main chat", worktreeId: "wt-main" })]));

    renderList();
    expect(screen.getByText("Main chat")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { expanded: true, name: /reliant/ }));
    expect(screen.queryByText("Main chat")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { expanded: false, name: /reliant/ }));
    expect(screen.getByText("Main chat")).toBeInTheDocument();
  });

  it("has exactly one new-chat control, in the header", () => {
    // The reported bug: the header `+` and the main group's `+` rendered as two
    // identical icons a few px apart. Which workspace a chat starts in is now
    // picked on /m/new, so no group carries its own new-chat button.
    useChatListWithWorkspaces.mockReturnValue(listOf([chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature" })]));
    renderList();

    // By label: the stubbed Link renders an <a> with no href, which has no
    // link role.
    const newChat = screen.getAllByLabelText(/New chat/);
    expect(newChat).toHaveLength(1);
    expect(newChat[0]).toHaveAttribute("to", "/m/new");
    expect(screen.queryByRole("button", { name: /New chat in/ })).not.toBeInTheDocument();
  });

  it("creates a workspace from the list and lands on the composer pointed at it", async () => {
    const user = userEvent.setup();
    useChatListWithWorkspaces.mockReturnValue(listOf([]));
    renderList();

    await user.click(screen.getByRole("button", { name: "New workspace" }));
    expect(screen.getByRole("dialog", { name: "New workspace" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Create stub" }));
    expect(navigate).toHaveBeenCalledWith({ to: "/m/new", search: { worktreeId: "wt-new" } });
    expect(screen.queryByRole("dialog", { name: "New workspace" })).not.toBeInTheDocument();
  });

  it("does not offer to archive the main workspace", () => {
    useChatListWithWorkspaces.mockReturnValue(listOf([]));
    renderList();
    expect(screen.queryByRole("button", { name: /Archive reliant/ })).not.toBeInTheDocument();
  });

  it("archives a non-main workspace after confirmation", async () => {
    const user = userEvent.setup();
    useChatListWithWorkspaces.mockReturnValue(listOf([chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature" })]));

    renderList();
    await user.click(screen.getByRole("button", { name: /Archive feature-x/ }));
    expect(await screen.findByRole("dialog", { name: "Confirm archive" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Archive" }));
    expect(archiveWorktree).toHaveBeenCalledWith("wt-feature");
  });

  it("invalidates the chat list so archived chats do not linger", async () => {
    // Archiving a worktree also archives its chats server-side, but only the
    // worktree store refetches on its own. Without this invalidation the
    // archived chats stayed in cache still pointing at a worktree that was
    // gone, and the grouping fell back to a ghost "Unknown workspace" row that
    // survived until a manual reload.
    const user = userEvent.setup();
    useChatListWithWorkspaces.mockReturnValue(listOf([chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature" })]));

    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const invalidate = vi.spyOn(client, "invalidateQueries");

    render(
      <QueryClientProvider client={client}>
        <VirtuosoMockContext.Provider
          value={{ viewportHeight: 1000, itemHeight: 64 }}
        >
          <MobileChatList />
        </VirtuosoMockContext.Provider>
      </QueryClientProvider>,
    );

    await user.click(screen.getByRole("button", { name: /Archive feature-x/ }));
    await user.click(screen.getByRole("button", { name: "Archive" }));

    await waitFor(() =>
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: ["chats", "list"],
      }),
    );
  });

  it("opens a workspace's Files and Git without a chat", async () => {
    const user = userEvent.setup();
    useChatListWithWorkspaces.mockReturnValue(listOf([chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature" })]));
    renderList();

    await user.click(screen.getByRole("button", { name: "Files and Git for feature-x" }));
    expect(await screen.findByRole("dialog", { name: "Workspace panels" })).toHaveTextContent(
      "feature-x:wt-feature:no-chat",
    );
  });

  it("offers no Files and Git for chats whose workspace is gone", () => {
    useChatListWithWorkspaces.mockReturnValue(listOf([chat({ id: "c1", title: "Orphan", worktreeId: "wt-deleted" })]));
    renderList();
    expect(screen.getByText("Unknown workspace")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Files and Git for Unknown workspace" }),
    ).not.toBeInTheDocument();
  });

  it("shows the archive in progress and ignores a second tap", async () => {
    // Archiving waits on the workspace's machine, which may be waking. The
    // sheet used to keep a live Archive button with no feedback meanwhile.
    const user = userEvent.setup();
    let finish!: () => void;
    archiveWorktree.mockReturnValue(new Promise<void>((resolve) => (finish = resolve)));
    useChatListWithWorkspaces.mockReturnValue(listOf([chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature" })]));

    renderList();
    await user.click(screen.getByRole("button", { name: /Archive feature-x/ }));
    await user.click(screen.getByRole("button", { name: "Archive" }));

    const pending = await screen.findByRole("button", { name: /Archiving/ });
    expect(pending).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    await user.click(pending);
    expect(archiveWorktree).toHaveBeenCalledTimes(1);

    finish();
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Confirm archive" })).not.toBeInTheDocument(),
    );
  });

  it("cancels archiving without calling archiveWorktree", async () => {
    const user = userEvent.setup();
    useChatListWithWorkspaces.mockReturnValue(listOf([chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature" })]));

    renderList();
    await user.click(screen.getByRole("button", { name: /Archive feature-x/ }));
    await user.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(archiveWorktree).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog", { name: "Confirm archive" })).not.toBeInTheDocument();
  });

  it("shows a loading state", () => {
    useChatListWithWorkspaces.mockReturnValue({ data: undefined, isLoading: true });
    renderList();
    expect(screen.queryByText("reliant")).not.toBeInTheDocument();
  });
});

describe("buildGroups across all projects", () => {
  it("names each group by its project and lists no empty main workspaces", () => {
    const groups = buildGroups(
      [
        { ...chat({ id: "a1", worktreeId: "wt-a" }), projectId: "pa", worktree: { ...FEATURE, id: "wt-a" } },
        { ...chat({ id: "b1" }), projectId: "pb", worktreeId: undefined, worktree: undefined },
      ] as never,
      [
        { ...MAIN, id: "main-pa", projectId: "pa" },
        { ...MAIN, id: "main-pb", projectId: "pb" },
      ] as never,
      "recent_activity",
      { scope: null, projectNames: new Map([["pa", "Alpha"], ["pb", "Beta"]]) },
    );
    expect(groups.map((g) => g.worktreeName).sort()).toEqual(["Alpha · feature-x", "Beta · reliant"]);
    expect(groups.some((g) => g.worktreeId === "main-pa")).toBe(false);
  });
});
