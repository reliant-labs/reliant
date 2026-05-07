import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import type { Chat } from "../../types/chat";

const unarchiveMock = vi.hoisted(() => vi.fn());
const listChatsMock = vi.hoisted(() => vi.fn());
const listWorktreesMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/client", () => ({
  api: {
    chatsV2: {
      list: listChatsMock,
      listMessages: vi.fn(async () => ({ messages: [], total: 0, hasMore: false, oldestOrdinal: 0 })),
      get: vi.fn(async () => ({})),
      sendMessage: vi.fn(async () => ({})),
      cancel: vi.fn(async () => ({ success: true })),
      pause: vi.fn(async () => ({ success: true })),
      resume: vi.fn(async () => ({ success: true })),
      delete: vi.fn(async () => ({ permanently_deleted: true })),
      update: vi.fn(async () => ({})),
      create: vi.fn(async () => ({})),
      branch: vi.fn(async () => ({ chat: {} })),
      listArchived: vi.fn(async () => []),
      unarchive: unarchiveMock,
      dismiss: vi.fn(async () => ({ changed: false })),
      markUnread: vi.fn(async () => ({ changed: false })),
      compact: vi.fn(async () => ({})),
      updateWorkflowParams: vi.fn(async () => ({})),
    },
    approvals: {
      listByChat: vi.fn(async () => []),
      approve: vi.fn(async () => ({})),
      deny: vi.fn(async () => ({})),
      batchApprove: vi.fn(async () => ({})),
      batchDeny: vi.fn(async () => ({})),
    },
    toolCalls: {
      cancel: vi.fn(async () => ({})),
      convertToBackground: vi.fn(async () => ({ process_id: "proc-1" })),
    },
  },
}));

vi.mock("../../api/worktree-grpc", () => ({
  worktreeGrpc: {
    list: listWorktreesMock,
  },
}));

import { useChatStore } from "../chatStore";
import { useProjectStore } from "../projectStore";
import { useWorktreeStore } from "../worktreeStore";

const projectId = "project-1";
const now = "2026-01-01T00:00:00.000Z";

function buildChat(overrides: Partial<Chat>): Chat {
  return {
    id: "chat-archived",
    userId: "user-1",
    title: "Archived chat",
    projectId,
    worktreeId: "wt-archived",
    state: ChatState.ARCHIVED,
    createdAt: now,
    updatedAt: now,
    lastActive: now,
    selectedPresets: {},
    needsRecovery: false,
    activity: 0,
    unread: false,
    ...overrides,
  } as Chat;
}

describe("chatStore unarchiveChat", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useChatStore.getState().reset();
    useProjectStore.setState({
      currentProject: {
        id: projectId,
        name: "Project",
        path: "/tmp/project",
        is_git_repo: true,
        worktree_count: 1,
        last_active: now,
        created_at: now,
        updated_at: now,
      },
    });
    useWorktreeStore.setState({
      worktrees: [],
      currentWorktree: null,
      discoveredWorktrees: [],
      isLoading: false,
      hasLoaded: true,
      isDiscovering: false,
      deletingId: null,
      error: null,
      lastLoadIncludedArchived: false,
    });
    unarchiveMock.mockResolvedValue({ message: "restored", worktree_restored: true });
    listWorktreesMock.mockResolvedValue({
      worktrees: [
        {
          id: "wt-archived",
          name: "Restored workspace",
          path: "/tmp/project/wt-archived",
          branch: "feature/restored",
          base_branch: "main",
          project_id: projectId,
          status: WorktreeStatus.ACTIVE,
          is_main: false,
          created_at: now,
          updated_at: now,
          last_active: now,
        },
      ],
    });
    listChatsMock.mockResolvedValue({
      chats: [buildChat({ state: ChatState.IDLE })],
      lastUserUpdateSequence: 0,
    });
  });

  it("moves a restored chat out of archived and into active state immediately", async () => {
    const archivedChat = buildChat({});
    useChatStore.setState({ archivedChats: [archivedChat], archivedChatsLoaded: true });

    await useChatStore.getState().unarchiveChat(archivedChat.id);

    const state = useChatStore.getState();
    expect(unarchiveMock).toHaveBeenCalledWith(archivedChat.id);
    expect(state.archivedChats.some((chat) => chat.id === archivedChat.id)).toBe(false);
    expect(state.chats.get(archivedChat.id)?.state).toBe(ChatState.IDLE);
    expect(state.chatOrder).toContain(archivedChat.id);
  });

  it("reloads worktrees and chats so active grouping can see the restored workspace", async () => {
    const archivedChat = buildChat({});
    useChatStore.setState({ archivedChats: [archivedChat], archivedChatsLoaded: true });

    await useChatStore.getState().unarchiveChat(archivedChat.id);

    expect(listWorktreesMock).toHaveBeenCalledWith(projectId, { includeArchived: false });
    expect(listChatsMock).toHaveBeenCalledWith(projectId);
    expect(useWorktreeStore.getState().worktrees.some((worktree) => worktree.id === archivedChat.worktreeId)).toBe(true);
  });

  it("rolls back optimistic active state when unarchive fails", async () => {
    const archivedChat = buildChat({});
    useChatStore.setState({ archivedChats: [archivedChat], archivedChatsLoaded: true });
    unarchiveMock.mockRejectedValueOnce(new Error("restore failed"));

    await expect(useChatStore.getState().unarchiveChat(archivedChat.id)).rejects.toThrow("restore failed");

    const state = useChatStore.getState();
    expect(state.archivedChats.some((chat) => chat.id === archivedChat.id)).toBe(true);
    expect(state.chats.has(archivedChat.id)).toBe(false);
  });
});
