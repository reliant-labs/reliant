// Copyright (c) 2025 Reliant Labs

/**
 * A sidebar row shows "needs your input", "still running" and "unread" as
 * three different indicators. They used to share one red dot (prod chat
 * adcee226 read as unread while two sub-agents ran and a third waited on a
 * question), with the running dot hidden whenever input was needed.
 */

import { render, screen, within } from "@testing-library/react";
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

describe("Sidebar activity indicators", () => {
  beforeEach(() => {
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
    expect(screen.getByTestId("chat-needs-input-running-asking")).toBeInTheDocument();
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
});
