import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { useChatNavigationStore } from "../chatNavigationStore";
import { useProjectStore } from "../projectStore";
import { useWorkspaceStateStore } from "../workspaceStateStore";
import { useWorktreeStore } from "../worktreeStore";
import { chatKeys } from "../../hooks/chat-queries";
import { queryClient } from "../../lib/query-client";
import { setWorktreesForTest } from "../../test/worktreeCache";

const selectChatMock = vi.fn();
const persistActiveChatMock = vi.fn();
const chatStoreGetStateMock = vi.hoisted(() => vi.fn());

vi.mock("../chatStore", () => ({
  useChatStore: {
    getState: chatStoreGetStateMock,
  },
}));

describe("chatNavigationStore worktree-aware navigation", () => {
  beforeEach(() => {
    // Navigation walks the sidebar's scope; these seed one project's list.
    useWorkspaceStateStore.setState({ projectFilterId: "project-1" });
    queryClient.clear();
    selectChatMock.mockReset();
    persistActiveChatMock.mockReset();
    chatStoreGetStateMock.mockReset();
    chatStoreGetStateMock.mockReturnValue({
      activeChatId: null,
      selectChat: selectChatMock,
      persistActiveChat: persistActiveChatMock,
      clearCurrentChat: vi.fn(),
    });
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
        is_forge: false,
        last_active: new Date().toISOString(),
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    });

    setWorktreesForTest({
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
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
          last_active: new Date().toISOString(),
        },
      ],
      currentWorktree: null,
    });
    useWorktreeStore.setState({
      switchWorktreeContext: vi.fn().mockResolvedValue(undefined),
    });
  });

  afterEach(() => {
    queryClient.clear();
  });

  it("selects the next chat without waiting for its worktree, then records it in that worktree", async () => {
    const nextChat = {
      id: "chat-2",
      title: "Next Chat",
      worktreeId: "wt-1",
      state: 0,
      unread: false,
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
      lastMessageAt: new Date().toISOString(),
    };

    const switchWorktreeContextMock = vi.fn().mockResolvedValue(undefined);
    useWorktreeStore.setState({ switchWorktreeContext: switchWorktreeContextMock });

    const activeChatId = { current: null as string | null };
    selectChatMock.mockImplementation((chat: { id: string }) => {
      activeChatId.current = chat.id;
    });
    chatStoreGetStateMock.mockImplementation(() => ({
      activeChatId: activeChatId.current,
      selectChat: selectChatMock,
      persistActiveChat: persistActiveChatMock,
      clearCurrentChat: vi.fn(),
    }));

    // Chats are sourced from the React Query list cache (single source of truth).
    queryClient.setQueryData(chatKeys.list("project-1"), {
      chats: [nextChat],
      total: 1,
      lastUserUpdateSequence: 1,
    });

    await useChatNavigationStore.getState().navigateNext();

    // Selection (and so the stream subscribe) does not wait on the worktree...
    expect(selectChatMock).toHaveBeenCalledWith(nextChat, { deferPersist: true });
    expect(selectChatMock.mock.invocationCallOrder[0]).toBeLessThan(
      switchWorktreeContextMock.mock.invocationCallOrder[0],
    );
    // ...but what is stored under a worktree is stored once that worktree is current.
    expect(switchWorktreeContextMock).toHaveBeenCalledWith(
      "project-1",
      expect.objectContaining({ id: "wt-1" }),
      { isCurrent: expect.any(Function) },
    );
    expect(persistActiveChatMock).toHaveBeenCalledWith("chat-2");
    expect(switchWorktreeContextMock.mock.invocationCallOrder[0]).toBeLessThan(
      persistActiveChatMock.mock.invocationCallOrder[0],
    );
  });
});