/**
 * Opening a chat, and starting one, in the global window.
 *
 * openChat must move the context project to the chat's own — without
 * reopening that project's last chat over it — then its workspace, then select
 * it. A global "New chat" starts in the sidebar filter's project, else the
 * open chat's, else the last one used.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  selectProject: vi.fn(async (project: { id: string }) => {
    mocks.state.currentProject = project;
  }),
  switchWorktreeContext: vi.fn(async () => {}),
  selectChat: vi.fn(),
  clearCurrentChat: vi.fn(),
  navigateToChat: vi.fn(),
  resolveWorktree: vi.fn(async (id: string) => ({ id, project_id: "p2" })),
  resolveMainWorktree: vi.fn(async (projectId: string) => ({ id: `main-${projectId}` })),
  state: {
    currentProject: { id: "p1" } as { id: string } | null,
    activeChatId: null as string | null,
  },
  projects: [
    { id: "p1", name: "one" },
    { id: "p2", name: "two" },
  ],
}));

vi.mock("../projectStore", () => ({
  useProjectStore: {
    getState: () => ({ currentProject: mocks.state.currentProject, selectProject: mocks.selectProject }),
  },
}));
vi.mock("../worktreeStore", () => ({
  useWorktreeStore: {
    getState: () => ({ switchWorktreeContext: mocks.switchWorktreeContext, currentWorktreeId: "wt-current" }),
  },
}));
vi.mock("../chatStore", () => ({
  useChatStore: {
    getState: () => ({
      selectChat: mocks.selectChat,
      clearCurrentChat: mocks.clearCurrentChat,
      activeChatId: mocks.state.activeChatId,
    }),
  },
}));
vi.mock("../chatNavigationStore", () => ({
  useChatNavigationStore: { getState: () => ({ navigateToChat: mocks.navigateToChat }) },
}));
vi.mock("../../hooks/project-queries", () => ({
  getCachedProjects: () => mocks.projects,
  fetchProjects: async () => mocks.projects,
}));
vi.mock("../../hooks/worktree-queries", () => ({
  resolveWorktree: (id: string) => mocks.resolveWorktree(id),
  resolveMainWorktree: (id: string) => mocks.resolveMainWorktree(id),
}));
vi.mock("../../hooks/chat-queries", () => ({
  getChatFromCache: () => undefined,
}));

import { useWorkspaceStateStore } from "../workspaceStateStore";
import { ChatProjectUnavailableError, newChatTargetProjectId, openChat, openNewChat } from "../openChat";

beforeEach(() => {
  vi.clearAllMocks();
  mocks.state.currentProject = { id: "p1" };
  mocks.state.activeChatId = null;
  useWorkspaceStateStore.setState({ projectFilterId: null, lastProjectId: null });
});

describe("openChat", () => {
  it("moves the context to the chat's project without restoring its last chat, then opens the chat", async () => {
    const chat = { id: "c9", projectId: "p2", worktreeId: "wt-9" } as never;
    await openChat(chat);

    expect(mocks.selectProject).toHaveBeenCalledWith(mocks.projects[1], { restoreChat: false });
    expect(mocks.switchWorktreeContext).toHaveBeenCalledWith("p2", { id: "wt-9", project_id: "p2" });
    expect(mocks.selectChat).toHaveBeenCalledWith(chat);
    expect(mocks.navigateToChat).toHaveBeenCalledWith("c9");
  });

  it("stays in the project when the chat is already in it", async () => {
    await openChat({ id: "c1", projectId: "p1" } as never);
    expect(mocks.selectProject).not.toHaveBeenCalled();
    expect(mocks.switchWorktreeContext).toHaveBeenCalledWith("p1", null);
  });

  it("refuses a chat whose project is gone", async () => {
    await expect(openChat({ id: "c1", projectId: "gone" } as never)).rejects.toBeInstanceOf(
      ChatProjectUnavailableError,
    );
    expect(mocks.selectChat).not.toHaveBeenCalled();
  });
});

describe("newChatTargetProjectId", () => {
  it("prefers the sidebar filter, then the open chat's project, then the last one used", () => {
    useWorkspaceStateStore.setState({ projectFilterId: "p2", lastProjectId: "p3" });
    expect(newChatTargetProjectId()).toBe("p2");

    useWorkspaceStateStore.setState({ projectFilterId: null });
    expect(newChatTargetProjectId()).toBe("p1");

    mocks.state.currentProject = null;
    expect(newChatTargetProjectId()).toBe("p3");
  });
});

describe("openNewChat", () => {
  it("starts in another project on its main workspace", async () => {
    await openNewChat({ projectId: "p2" });
    expect(mocks.selectProject).toHaveBeenCalledWith(mocks.projects[1], { restoreChat: false });
    expect(mocks.switchWorktreeContext).toHaveBeenCalledWith("p2", { id: "main-p2" });
    expect(mocks.clearCurrentChat).toHaveBeenCalledWith("main-p2");
  });

  it("keeps the current workspace when the project does not change", async () => {
    await openNewChat();
    expect(mocks.selectProject).not.toHaveBeenCalled();
    expect(mocks.clearCurrentChat).toHaveBeenCalledWith("wt-current");
  });
});
