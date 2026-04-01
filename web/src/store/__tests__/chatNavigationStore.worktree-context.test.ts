import { beforeEach, describe, expect, it, vi } from "vitest";
import { useChatNavigationStore } from "../chatNavigationStore";
import { useProjectStore } from "../projectStore";
import { useWorktreeStore } from "../worktreeStore";

const selectChatMock = vi.fn();
const chatStoreGetStateMock = vi.hoisted(() => vi.fn());

vi.mock("../chatStore", () => ({
  useChatStore: {
    getState: chatStoreGetStateMock,
  },
}));

describe("chatNavigationStore worktree-aware navigation", () => {
  beforeEach(() => {
    selectChatMock.mockReset();
    chatStoreGetStateMock.mockReset();
    chatStoreGetStateMock.mockReturnValue({
      activeChatId: null,
      chats: new Map(),
      selectChat: selectChatMock,
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
        worktree_count: 1,
        last_active: new Date().toISOString(),
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    });

    useWorktreeStore.setState({
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
      discoveredWorktrees: [],
      isLoading: false,
      isDiscovering: false,
      deletingId: null,
      error: null,
      loadWorktrees: vi.fn(),
      selectWorktree: vi.fn(),
      createWorktree: vi.fn(),
      importWorktree: vi.fn(),
      discoverWorktrees: vi.fn(),
      archiveWorktree: vi.fn(),
      deleteWorktree: vi.fn(),
      unarchiveWorktree: vi.fn(),
      updateWorktreeStatus: vi.fn(),
      restoreLastWorktree: vi.fn(),
      switchWorktreeContext: vi.fn().mockResolvedValue(undefined),
      reset: vi.fn(),
    });
  });

  it("switches worktree context before selecting the next chat", async () => {
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
    useWorktreeStore.setState((state) => ({
      ...state,
      worktrees: [state.worktrees[0]],
      switchWorktreeContext: switchWorktreeContextMock,
    }));

    chatStoreGetStateMock.mockReturnValue({
      activeChatId: null,
      chats: new Map([[nextChat.id, nextChat]]),
      selectChat: selectChatMock,
      clearCurrentChat: vi.fn(),
    });

    await useChatNavigationStore.getState().navigateNext();

    expect(switchWorktreeContextMock).toHaveBeenCalledWith("project-1", expect.objectContaining({ id: "wt-1" }));
    expect(selectChatMock).toHaveBeenCalledWith(nextChat);
    expect(switchWorktreeContextMock.mock.invocationCallOrder[0]).toBeLessThan(
      selectChatMock.mock.invocationCallOrder[0],
    );
  });
});
