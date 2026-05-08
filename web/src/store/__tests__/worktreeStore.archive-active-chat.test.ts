import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import type { Chat } from "../../api/client";
import type { Worktree } from "../worktreeStore";

const archiveMock = vi.hoisted(() => vi.fn());
const deleteMock = vi.hoisted(() => vi.fn());
const listMock = vi.hoisted(() => vi.fn());
const loadChatsMock = vi.hoisted(() => vi.fn());
const clearCurrentChatMock = vi.hoisted(() => vi.fn());
const closeWorktreeTabsMock = vi.hoisted(() => vi.fn());
const toastSuccessMock = vi.hoisted(() => vi.fn());
const toastErrorMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/worktree-grpc", () => ({
  worktreeGrpc: {
    archive: archiveMock,
    delete: deleteMock,
    list: listMock,
  },
}));

vi.mock("../../lib/toast-manager", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

vi.mock("../browserStore", () => ({
  useBrowserStore: {
    getState: () => ({
      closeWorktreeTabs: closeWorktreeTabsMock,
    }),
  },
}));

import { useChatNavigationStore } from "../chatNavigationStore";
import { useChatStore } from "../chatStore";
import { useWorkspaceStateStore } from "../workspaceStateStore";
import { useWorktreeStore } from "../worktreeStore";

const projectId = "project-1";
const now = "2026-01-01T00:00:00.000Z";

function buildWorktree(overrides: Partial<Worktree>): Worktree {
  return {
    id: "wt-feature",
    name: "Feature workspace",
    path: "/tmp/project/wt-feature",
    branch: "feature/archive",
    base_branch: "main",
    project_id: projectId,
    status: WorktreeStatus.UNSPECIFIED,
    is_main: false,
    created_at: now,
    updated_at: now,
    last_active: now,
    deleted_at: null,
    ...overrides,
  };
}

function buildChat(overrides: Partial<Chat>): Chat {
  return {
    id: "chat-feature",
    userId: "user-1",
    title: "Feature chat",
    projectId,
    worktreeId: "wt-feature",
    state: ChatState.IDLE,
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

function resetStores() {
  useChatNavigationStore.setState({
    chatQueue: ["chat-feature", "chat-main"],
    showRecentChanges: {},
    scrollPosition: {},
  });

  useWorkspaceStateStore.getState().reset();
  useWorkspaceStateStore.getState().setLastWorktree(projectId, "wt-feature");
  useWorkspaceStateStore.getState().setActiveChatId(projectId, "wt-feature", "chat-feature");
  useWorkspaceStateStore.getState().setActiveChatId(projectId, "wt-main", "chat-main");

  useChatStore.setState({
    activeChatId: "chat-feature",
    chats: new Map([
      ["chat-feature", buildChat({})],
      ["chat-main", buildChat({ id: "chat-main", title: "Main chat", worktreeId: "wt-main" })],
    ]),
    isLoading: false,
    error: null,
  });

  const mainWorktree = buildWorktree({
    id: "wt-main",
    name: "Main workspace",
    path: "/tmp/project",
    branch: "main",
    is_main: true,
  });
  const featureWorktree = buildWorktree({});

  useWorktreeStore.setState({
    worktrees: [mainWorktree, featureWorktree],
    currentWorktree: featureWorktree,
    discoveredWorktrees: [],
    isLoading: false,
    hasLoaded: true,
    isDiscovering: false,
    deletingId: null,
    error: null,
    lastLoadIncludedArchived: false,
  });

  listMock.mockResolvedValue({
    worktrees: [
      {
        id: "wt-main",
        name: "Main workspace",
        path: "/tmp/project",
        branch: "main",
        base_branch: "main",
        project_id: projectId,
        status: WorktreeStatus.UNSPECIFIED,
        is_main: true,
        created_at: now,
        updated_at: now,
        last_active: now,
      },
    ],
  });
}

describe("worktreeStore archive active chat behavior", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    archiveMock.mockResolvedValue(undefined);
    deleteMock.mockResolvedValue(undefined);
    loadChatsMock.mockResolvedValue(undefined);
    clearCurrentChatMock.mockImplementation((worktreeId?: string | null) => {
      const activeChatId = useChatStore.getState().activeChatId;
      useChatStore.setState({ activeChatId: null });
      if (activeChatId) {
        const targetWorktreeId = worktreeId ?? useWorktreeStore.getState().currentWorktree?.id ?? null;
        useWorkspaceStateStore.getState().setActiveChatId(projectId, targetWorktreeId, null);
      }
    });
    useChatStore.setState({
      loadChats: loadChatsMock,
      clearCurrentChat: clearCurrentChatMock,
    });
    resetStores();
  });

  it("clears an active archived-workspace chat even when that chat remains loaded", async () => {
    await useWorktreeStore.getState().archiveWorktree("wt-feature");

    expect(clearCurrentChatMock).toHaveBeenCalledWith("wt-feature");
    expect(useChatStore.getState().activeChatId).toBeNull();
    expect(useChatNavigationStore.getState().chatQueue).not.toContain("chat-feature");
    expect(useWorktreeStore.getState().currentWorktree?.id).toBe("wt-main");
    expect(useWorkspaceStateStore.getState().getWorktreeState(projectId, "wt-main").activeChatId).toBeNull();
  });

  it("opens a fresh main-workspace new chat after archiving the current workspace", async () => {
    const switchWorktreeContextSpy = vi.spyOn(useWorktreeStore.getState(), "switchWorktreeContext");

    await useWorktreeStore.getState().archiveWorktree("wt-feature");

    expect(switchWorktreeContextSpy).toHaveBeenCalledWith(
      projectId,
      expect.objectContaining({ id: "wt-main" }),
      { openFreshNewChat: true }
    );
  });

  it("uses the same fresh-main behavior for deleteWorktree's non-permanent archive path", async () => {
    const switchWorktreeContextSpy = vi.spyOn(useWorktreeStore.getState(), "switchWorktreeContext");

    await useWorktreeStore.getState().deleteWorktree("wt-feature");

    expect(clearCurrentChatMock).toHaveBeenCalledWith("wt-feature");
    expect(switchWorktreeContextSpy).toHaveBeenCalledWith(
      projectId,
      expect.objectContaining({ id: "wt-main" }),
      { openFreshNewChat: true }
    );
  });
});
