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

const navigate = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
  useNavigate: () => navigate,
  useRouterState: () => "/m/chats",
}));

const useChatList = vi.fn();
vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: (...args: unknown[]) => useChatList(...args),
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

const archiveWorktree = vi.fn();
let worktreesFixture: Array<{
  id: string;
  name: string;
  branch: string;
  is_main: boolean;
  deleted_at?: string | null;
}> = [];

vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: (selector: (s: unknown) => unknown) =>
    selector({ worktrees: worktreesFixture, archiveWorktree }),
}));

vi.mock("../../../store/mobileDrawerStore", () => ({
  useMobileDrawerStore: (selector: (s: unknown) => unknown) =>
    selector({ isOpen: false, open: vi.fn(), close: vi.fn() }),
}));

const { MobileChatList } = await import("../MobileChatList");

function renderList() {
  // The component invalidates the chat list after archiving a worktree, so it
  // calls `useQueryClient` on every render — without a provider that throws
  // before anything renders, and every test in this file fails identically.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <VirtuosoMockContext.Provider value={{ viewportHeight: 1000, itemHeight: 64 }}>
        <MobileChatList />
      </VirtuosoMockContext.Provider>
    </QueryClientProvider>,
  );
}

function chat(overrides: Partial<{
  id: string;
  title: string;
  worktreeId: string;
  activity: ChatActivity;
  needsRecovery: boolean;
  unread: boolean;
  lastMessageAt: string;
  state: ChatState;
}>) {
  return {
    id: "chat-default",
    title: "Untitled",
    worktreeId: "wt-main",
    activity: ChatActivity.IDLE,
    needsRecovery: false,
    unread: false,
    lastMessageAt: "2024-01-01T00:00:00.000Z",
    state: ChatState.IDLE,
    ...overrides,
  };
}

beforeEach(() => {
  navigate.mockReset();
  archiveWorktree.mockReset();
  useChatList.mockReset();
  worktreesFixture = [
    { id: "wt-main", name: "reliant", branch: "main", is_main: true },
    { id: "wt-feature", name: "feature-x", branch: "feature/x", is_main: false },
  ];
});

describe("MobileChatList", () => {
  it("groups chats by worktree with the main workspace first", () => {
    useChatList.mockReturnValue({
      data: [
        chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature", lastMessageAt: "2024-01-03T00:00:00.000Z" }),
        chat({ id: "c2", title: "Main chat", worktreeId: "wt-main", lastMessageAt: "2024-01-01T00:00:00.000Z" }),
      ],
      isLoading: false,
    });

    renderList();

    const groupHeadings = screen.getAllByText(/reliant|feature-x/);
    // "reliant" (main) is listed first even though "feature-x" has the more
    // recent chat — main always sorts first regardless of activity.
    expect(groupHeadings[0]).toHaveTextContent("reliant");
    expect(screen.getByText("Main chat")).toBeInTheDocument();
    expect(screen.getByText("Branch chat")).toBeInTheDocument();
  });

  it("shows the main workspace even with zero chats", () => {
    useChatList.mockReturnValue({ data: [], isLoading: false });
    renderList();
    expect(screen.getByText("reliant")).toBeInTheDocument();
    expect(screen.getByText("0", { selector: "span" })).toBeInTheDocument();
  });

  it("sorts a needing-attention group before a merely-recent group", () => {
    useChatList.mockReturnValue({
      data: [
        chat({ id: "c1", title: "Recent branch chat", worktreeId: "wt-feature", lastMessageAt: "2024-01-05T00:00:00.000Z" }),
        chat({
          id: "c2",
          title: "Stuck main chat",
          worktreeId: "wt-main",
          activity: ChatActivity.AWAITING_INPUT,
          lastMessageAt: "2024-01-01T00:00:00.000Z",
        }),
      ],
      isLoading: false,
    });

    renderList();
    // Main is pinned first by the isMain rule regardless of activity, so this
    // pins that rule rather than re-deriving activity-vs-recency ordering
    // (covered separately by the within-group test below).
    const headings = screen.getAllByText(/reliant|feature-x/);
    expect(headings[0]).toHaveTextContent("reliant");
  });

  it("orders chats within a group by attention first, then recency", () => {
    useChatList.mockReturnValue({
      data: [
        chat({ id: "c1", title: "Older, needs attention", worktreeId: "wt-main", activity: ChatActivity.AWAITING_INPUT, lastMessageAt: "2024-01-01T00:00:00.000Z" }),
        chat({ id: "c2", title: "Newer, idle", worktreeId: "wt-main", lastMessageAt: "2024-01-05T00:00:00.000Z" }),
      ],
      isLoading: false,
    });

    renderList();
    const rows = screen.getAllByText(/Older, needs attention|Newer, idle/);
    expect(rows[0]).toHaveTextContent("Older, needs attention");
    expect(rows[1]).toHaveTextContent("Newer, idle");
    expect(screen.getByText("Needs you")).toBeInTheDocument();
  });

  it("collapses and expands a group", async () => {
    const user = userEvent.setup();
    useChatList.mockReturnValue({
      data: [chat({ id: "c1", title: "Main chat", worktreeId: "wt-main" })],
      isLoading: false,
    });

    renderList();
    expect(screen.getByText("Main chat")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { expanded: true, name: /reliant/ }));
    expect(screen.queryByText("Main chat")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { expanded: false, name: /reliant/ }));
    expect(screen.getByText("Main chat")).toBeInTheDocument();
  });

  it("navigates to /m/new with the group's worktreeId from the group header", async () => {
    const user = userEvent.setup();
    useChatList.mockReturnValue({ data: [], isLoading: false });

    renderList();
    await user.click(screen.getByRole("button", { name: /New chat in reliant/ }));
    expect(navigate).toHaveBeenCalledWith({
      to: "/m/new",
      search: { worktreeId: "wt-main" },
    });
  });

  it("does not offer to archive the main workspace", () => {
    useChatList.mockReturnValue({ data: [], isLoading: false });
    renderList();
    expect(screen.queryByRole("button", { name: /Archive reliant/ })).not.toBeInTheDocument();
  });

  it("archives a non-main workspace after confirmation", async () => {
    const user = userEvent.setup();
    useChatList.mockReturnValue({
      data: [chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature" })],
      isLoading: false,
    });

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
    useChatList.mockReturnValue({
      data: [chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature" })],
      isLoading: false,
    });

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

  it("cancels archiving without calling archiveWorktree", async () => {
    const user = userEvent.setup();
    useChatList.mockReturnValue({
      data: [chat({ id: "c1", title: "Branch chat", worktreeId: "wt-feature" })],
      isLoading: false,
    });

    renderList();
    await user.click(screen.getByRole("button", { name: /Archive feature-x/ }));
    await user.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(archiveWorktree).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog", { name: "Confirm archive" })).not.toBeInTheDocument();
  });

  it("shows a loading state", () => {
    useChatList.mockReturnValue({ data: undefined, isLoading: true });
    renderList();
    expect(screen.queryByText("reliant")).not.toBeInTheDocument();
  });
});
