// Copyright (c) 2025 Reliant Labs

/**
 * A sidebar row shows "needs your input", "still running" and "unread" as
 * three different indicators. They used to share one red dot (prod chat
 * adcee226 read as unread while two sub-agents ran and a third waited on a
 * question), with the running dot hidden whenever input was needed.
 */

import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";

import { ChatActivity, ChatState } from "../../../gen/reliant/v1/chat_pb";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";
import { useChatStore } from "../../../store/chatStore";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { useProjectStore } from "../../../store/projectStore";
import { useActivityStore } from "../../../store/activityStore";

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

// The incident's shapes: a chat whose sub-agent asked a question while two
// others keep running (adcee226), an unread chat, a running chat, and a chat
// that needs input with nothing else running and also has unread messages.
const rows = {
  asking: { activity: ChatActivity.AWAITING_INPUT, workingThreads: 2, unread: false },
  unread: { activity: ChatActivity.IDLE, workingThreads: 0, unread: true },
  running: { activity: ChatActivity.RUNNING, workingThreads: 1, unread: false },
  blocked: { activity: ChatActivity.AWAITING_INPUT, workingThreads: 0, unread: true },
  failed: { activity: ChatActivity.ERROR, workingThreads: 0, unread: false },
} as const;

const chats: Array<Record<string, unknown>> = Object.entries(rows).map(([id, row]) => ({
  id,
  worktree: workspace,
  title: `Chat ${id}`,
  createdAt: "2024-01-01T00:00:00.000Z",
  updatedAt: "2024-01-01T00:00:00.000Z",
  lastMessageAt: "2024-01-01T00:00:00.000Z",
  unread: row.unread,
  activity: row.activity,
  workingThreads: row.workingThreads,
  state: ChatState.IDLE,
  worktreeId: "worktree-1",
  projectId: "project-1",
}));

// Stable, as a query result is: a new array each render would re-run the
// sidebar's effects on every render.
const NO_MAIN_WORKTREES: never[] = [];

const LIST = { data: { chats, lastUserUpdateSequence: 0, mainWorktrees: NO_MAIN_WORKTREES } };
const listState = vi.hoisted(() => ({
  mode: "loaded" as "loaded" | "pending" | "failed",
  refetch: vi.fn(),
}));
vi.mock("../../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () =>
    listState.mode === "loaded"
      ? { ...LIST, isPending: false, isError: false, refetch: listState.refetch }
      : {
          data: undefined,
          isPending: listState.mode === "pending",
          isError: listState.mode === "failed",
          refetch: listState.refetch,
        },
  useArchivedChats: () => ({ data: [], isFetched: true }),
  usePermanentlyDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn(), mutate: vi.fn() }),
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

describe("Sidebar activity indicators", () => {
  beforeEach(() => {
    listState.mode = "loaded";
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
    useChatStore.setState({ activeChatId: null, selectChat: vi.fn() } as Partial<
      ReturnType<typeof useChatStore.getState>
    >);
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
    // What the ListChats snapshot writes into the activity store.
    useActivityStore.setState({ entries: new Map(), activities: new Map(), workingThreads: new Map(), maxSeenSeq: 0 });
    useActivityStore.getState().applyListActivities(
      new Map(Object.entries(rows).map(([id, row]) => [id, row.activity])),
      1,
      new Map(Object.entries(rows).map(([id, row]) => [id, row.workingThreads])),
    );
  });

  function renderSidebar() {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar />
      </QueryClientProvider>,
    );
  }

  it("draws needs-input with its own indicator and label, and keeps the running dot beside it", async () => {
    renderSidebar();
    const needsInput = await screen.findByTestId("chat-needs-input-asking");
    expect(within(needsInput).getByRole("img", { name: "Needs your input · 2 agents still running" })).toBeInTheDocument();
    expect(screen.getByTestId("chat-needs-input-running-asking")).toHaveClass("animate-spin");
    // Needs-input is not unread: no unread dot on a chat that has none.
    expect(screen.queryByTestId("chat-unread-dot-asking")).not.toBeInTheDocument();
  });

  it("draws unread as the unread dot only", async () => {
    renderSidebar();
    const dot = await screen.findByTestId("chat-unread-dot-unread");
    expect(dot).toHaveAccessibleName("Unread messages");
    expect(screen.queryByTestId("chat-needs-input-unread")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-activity-dot-unread")).not.toBeInTheDocument();
  });

  it("draws a running chat with the run dot only", async () => {
    renderSidebar();
    expect(await screen.findByTestId("chat-activity-dot-running")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-needs-input-running")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-unread-dot-running")).not.toBeInTheDocument();
  });

  it("shows needs-input and unread together, with no running dot when nothing runs", async () => {
    renderSidebar();
    const needsInput = await screen.findByTestId("chat-needs-input-blocked");
    expect(within(needsInput).getByRole("img", { name: "Needs your input" })).toBeInTheDocument();
    expect(screen.queryByTestId("chat-needs-input-running-blocked")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-unread-dot-blocked")).toBeInTheDocument();
  });

  it("labels a failed chat Failed, in a chip, and paints no row green", async () => {
    renderSidebar();
    const failed = await screen.findByTestId("chat-activity-dot-failed");
    expect(within(failed).getByRole("img", { name: "Failed" })).toBeInTheDocument();
    expect(failed).toHaveTextContent("Failed");
    // Running is a small spinner, not a chip with a word in it.
    const running = screen.getByTestId("chat-activity-dot-running");
    expect(running).toHaveAccessibleName("Running");
    expect(running).toHaveAttribute("data-tone", "running");
    expect(running).toHaveClass("animate-spin");
    expect(running.querySelector("title")).toHaveTextContent("Running");
    expect(running.tagName.toLowerCase()).toBe("svg"); // the icon itself: no chip box around it
    // Green means success; no row, chip or dot here is one.
    for (const id of Object.keys(rows)) {
      const row = document.querySelector(`[data-chat-id="${id}"]`)!;
      expect(row.outerHTML).not.toMatch(/success/);
    }
  });

  it("makes each row a button, one tab stop, and moves between rows with the arrow keys", async () => {
    renderSidebar();
    await screen.findByTestId("chat-activity-dot-running");
    const rowButtons = Array.from(document.querySelectorAll<HTMLButtonElement>("[data-chat-row]"));
    expect(rowButtons.length).toBe(Object.keys(rows).length);
    expect(rowButtons.every((b) => b.tagName === "BUTTON")).toBe(true);
    expect(rowButtons.filter((b) => b.tabIndex === 0)).toHaveLength(1);

    rowButtons[0].focus();
    fireEvent.keyDown(rowButtons[0], { key: "ArrowDown" });
    expect(rowButtons[1]).toHaveFocus();
    fireEvent.keyDown(rowButtons[1], { key: "End" });
    expect(rowButtons[rowButtons.length - 1]).toHaveFocus();
    fireEvent.keyDown(rowButtons[rowButtons.length - 1], { key: "ArrowUp" });
    expect(rowButtons[rowButtons.length - 2]).toHaveFocus();
  });

  it("names its hover controls", async () => {
    renderSidebar();
    await screen.findByTestId("chat-activity-dot-running");
    expect(screen.getByRole("button", { name: "Archive Chat running" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "More options for Chat running" })).toBeInTheDocument();
  });

  it("shows loading rows, not \"No active chats\", before the list answers", () => {
    listState.mode = "pending";
    renderSidebar();
    expect(screen.getByTestId("chat-list-loading")).toBeInTheDocument();
    expect(screen.queryByText("No active chats")).not.toBeInTheDocument();
  });

  it("says the list failed to load, with a Retry, rather than that there are no chats", () => {
    listState.mode = "failed";
    renderSidebar();
    expect(screen.getByRole("alert")).toHaveTextContent("Couldn't load your chats");
    expect(screen.queryByText("No active chats")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(listState.refetch).toHaveBeenCalled();
  });
});
