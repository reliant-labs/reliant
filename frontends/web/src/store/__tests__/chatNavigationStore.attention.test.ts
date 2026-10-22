/**
 * "Jump to the next chat that needs me."
 *
 * With a dozen chats running, next/prev is the wrong tool for triage — it walks
 * everything, including the chats happily working away. These cover the triage
 * move: only chats blocked on the user, in sidebar order, cycling and wrapping
 * so repeated presses drain the queue instead of bouncing between two.
 */

import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { useChatNavigationStore } from "../chatNavigationStore";
import { useProjectStore } from "../projectStore";
import { useWorktreeStore } from "../worktreeStore";
import { useActivityStore, ChatActivity } from "../activityStore";
import { chatKeys } from "../../hooks/chat-queries";
import { queryClient } from "../../lib/query-client";

const selectChatMock = vi.fn();
const chatStoreGetStateMock = vi.hoisted(() => vi.fn());

vi.mock("../chatStore", () => ({
  useChatStore: { getState: chatStoreGetStateMock },
}));

const TIMESTAMP = new Date("2026-01-01T00:00:00Z").toISOString();

function makeChat(id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    title: id,
    worktreeId: "wt-1",
    state: 0,
    unread: false,
    createdAt: TIMESTAMP,
    updatedAt: TIMESTAMP,
    lastMessageAt: TIMESTAMP,
    ...overrides,
  };
}

/** Seed the chat list cache the sidebar and navigation both read from. */
function seedChats(chats: ReturnType<typeof makeChat>[]) {
  queryClient.setQueryData(chatKeys.list("project-1"), {
    chats,
    total: chats.length,
    lastUserUpdateSequence: 1,
  });
}

function setActive(chatId: string | null) {
  chatStoreGetStateMock.mockReturnValue({
    activeChatId: chatId,
    selectChat: selectChatMock,
    clearCurrentChat: vi.fn(),
  });
}

describe("navigateToNextAttention", () => {
  beforeEach(() => {
    queryClient.clear();
    selectChatMock.mockReset();
    chatStoreGetStateMock.mockReset();
    setActive(null);

    useActivityStore.setState({ activities: new Map() });

    useChatNavigationStore.setState({
      chatQueue: [],
      showRecentChanges: {},
      scrollPosition: {},
    });

    useProjectStore.setState({
      currentProject: {
        id: "project-1",
        name: "Project",
        path: "/tmp/project",
        is_git_repo: true,
        worktree_count: 1,
        last_active: TIMESTAMP,
        created_at: TIMESTAMP,
        updated_at: TIMESTAMP,
      },
    });

    useWorktreeStore.setState((state) => ({
      ...state,
      worktrees: [
        {
          id: "wt-1",
          name: "Workspace 1",
          path: "/tmp/project/wt-1",
          branch: "feature/a",
          base_branch: "main",
          project_id: "project-1",
          status: 0,
          is_main: false,
          created_at: TIMESTAMP,
          updated_at: TIMESTAMP,
          last_active: TIMESTAMP,
        },
      ],
      currentWorktree: null,
      switchWorktreeContext: vi.fn().mockResolvedValue(undefined),
    }));
  });

  afterEach(() => {
    queryClient.clear();
  });

  it("skips busy chats and lands on the one awaiting approval", async () => {
    seedChats([makeChat("chat-1"), makeChat("chat-2"), makeChat("chat-3")]);
    useActivityStore.setState({
      activities: new Map([
        ["chat-1", ChatActivity.RUNNING],
        ["chat-3", ChatActivity.AWAITING_INPUT],
      ]),
    });
    setActive("chat-1");

    const found = await useChatNavigationStore
      .getState()
      .navigateToNextAttention();

    expect(found).toBe(true);
    expect(selectChatMock).toHaveBeenCalledWith(
      expect.objectContaining({ id: "chat-3" }),
    );
  });

  it("treats errored and unread chats as needing attention", async () => {
    seedChats([makeChat("chat-1"), makeChat("chat-2", { unread: true })]);
    useActivityStore.setState({
      activities: new Map([["chat-1", ChatActivity.RUNNING]]),
    });
    setActive("chat-1");

    await useChatNavigationStore.getState().navigateToNextAttention();
    expect(selectChatMock).toHaveBeenCalledWith(
      expect.objectContaining({ id: "chat-2" }),
    );

    selectChatMock.mockReset();
    seedChats([makeChat("chat-1"), makeChat("chat-2")]);
    useActivityStore.setState({
      activities: new Map([
        ["chat-1", ChatActivity.RUNNING],
        ["chat-2", ChatActivity.ERROR],
      ]),
    });

    await useChatNavigationStore.getState().navigateToNextAttention();
    expect(selectChatMock).toHaveBeenCalledWith(
      expect.objectContaining({ id: "chat-2" }),
    );
  });

  it("cycles through waiting chats rather than sticking on the first", async () => {
    // Two chats waiting: pressing the shortcut from the first must advance to
    // the second, not re-select the one already on screen.
    seedChats([
      makeChat("chat-1", { unread: true }),
      makeChat("chat-2", { unread: true }),
    ]);
    setActive("chat-1");

    await useChatNavigationStore.getState().navigateToNextAttention();

    expect(selectChatMock).toHaveBeenCalledWith(
      expect.objectContaining({ id: "chat-2" }),
    );
  });

  it("wraps around to the top of the list", async () => {
    seedChats([
      makeChat("chat-1", { unread: true }),
      makeChat("chat-2"),
      makeChat("chat-3"),
    ]);
    setActive("chat-3");

    await useChatNavigationStore.getState().navigateToNextAttention();

    expect(selectChatMock).toHaveBeenCalledWith(
      expect.objectContaining({ id: "chat-1" }),
    );
  });

  it("reports nothing to do when no chat is waiting", async () => {
    seedChats([makeChat("chat-1"), makeChat("chat-2")]);
    useActivityStore.setState({
      activities: new Map([["chat-1", ChatActivity.RUNNING]]),
    });
    setActive("chat-1");

    const found = await useChatNavigationStore
      .getState()
      .navigateToNextAttention();

    expect(found).toBe(false);
    expect(selectChatMock).not.toHaveBeenCalled();
  });

  it("stays put when the active chat is the only one waiting", async () => {
    // Reporting success without navigating keeps the caller from claiming
    // "nothing needs you" while the current chat is visibly blocked.
    seedChats([makeChat("chat-1", { unread: true }), makeChat("chat-2")]);
    setActive("chat-1");

    const found = await useChatNavigationStore
      .getState()
      .navigateToNextAttention();

    expect(found).toBe(true);
    expect(selectChatMock).not.toHaveBeenCalled();
  });
});
