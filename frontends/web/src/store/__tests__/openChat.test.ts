/**
 * Opening a chat, and starting one, in the global window.
 *
 * openChat selects immediately and moves context under the shared fence,
 * without reopening a project's last chat over it.
 * A global "New chat" starts in the sidebar filter's project, else the
 * open chat's, else the last one used.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  selectProject: vi.fn(async (project: { id: string }) => {
    mocks.state.currentProject = project;
  }),
  switchWorktreeContext: vi.fn(async () => {}),
  selectChat: vi.fn((chat: { id: string }) => { mocks.state.activeChatId = chat.id; }),
  persistActiveChat: vi.fn(),
  clearCurrentChat: vi.fn(() => { mocks.state.activeChatId = null; }),
  fetchProjects: vi.fn(async () => mocks.projects),
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
      persistActiveChat: mocks.persistActiveChat,
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
  fetchProjects: () => mocks.fetchProjects(),
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
import { openChatInWorkspace } from "../../lib/openChatInWorkspace";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

// Source entry points and main's helper must share one identity fence.
describe("deferred workspace opens", () => {
  it("selects a source chat before its project lookup, without changing the global filter", async () => {
    const pending = deferred<typeof mocks.projects>();
    mocks.fetchProjects.mockReturnValueOnce(pending.promise);
    useWorkspaceStateStore.setState({ projectFilterId: "p1" });
    const opening = openChat({ id: "a", projectId: "remote" } as never);
    expect(mocks.state.activeChatId).toBe("a");
    expect(mocks.selectProject).not.toHaveBeenCalled();
    pending.resolve([{ id: "remote", name: "remote" }]);
    await opening;
    expect(mocks.state.currentProject?.id).toBe("remote");
    expect(mocks.persistActiveChat).toHaveBeenCalledWith("a");
    expect(useWorkspaceStateStore.getState().projectFilterId).toBe("p1");
  });

  it("does not commit a late source project A after main opens B", async () => {
    const pending = deferred<typeof mocks.projects>();
    mocks.fetchProjects.mockReturnValueOnce(pending.promise);
    const a = openChat({ id: "a", projectId: "remote" } as never);
    await openChatInWorkspace({ id: "b", projectId: "p1" } as never);
    pending.resolve([{ id: "remote", name: "remote" }]);
    await a;
    expect(mocks.state.activeChatId).toBe("b");
    expect(mocks.state.currentProject?.id).toBe("p1");
    expect(mocks.selectProject).not.toHaveBeenCalled();
    expect(mocks.persistActiveChat).not.toHaveBeenCalledWith("a");
    expect(mocks.navigateToChat).not.toHaveBeenCalledWith("a");
  });

  it("does not commit a late source workspace A after source opens B", async () => {
    const pending = deferred<{ id: string; project_id: string }>();
    mocks.resolveWorktree.mockReturnValueOnce(pending.promise);
    const a = openChat({ id: "a", projectId: "p1", worktreeId: "wa" } as never);
    await openChat({ id: "b", projectId: "p2" } as never);
    pending.resolve({ id: "wa", project_id: "p1" });
    await a;
    expect(mocks.state.activeChatId).toBe("b");
    expect(mocks.state.currentProject?.id).toBe("p2");
    expect(mocks.switchWorktreeContext).not.toHaveBeenCalledWith("p1", expect.anything(), expect.anything());
    expect(mocks.persistActiveChat).not.toHaveBeenCalledWith("a");
  });

  it("fences new-chat project lookup against a later chat open", async () => {
    const pending = deferred<typeof mocks.projects>();
    mocks.fetchProjects.mockReturnValueOnce(pending.promise);
    const a = openNewChat({ projectId: "remote" });
    await openChat({ id: "b", projectId: "p1" } as never);
    pending.resolve([{ id: "remote", name: "remote" }]);
    await a;
    expect(mocks.state.currentProject?.id).toBe("p1");
    expect(mocks.state.activeChatId).toBe("b");
    expect(mocks.clearCurrentChat).not.toHaveBeenCalled();
  });

  it("fences new-chat worktree lookup against a later new-chat intent with the same identity", async () => {
    const pending = deferred<{ id: string; project_id: string }>();
    mocks.resolveWorktree.mockReturnValueOnce(pending.promise);
    const a = openNewChat({ projectId: "p1", worktreeId: "wa" });
    await openNewChat({ projectId: "p1" });
    pending.resolve({ id: "wa", project_id: "p1" });
    await a;
    expect(mocks.switchWorktreeContext).not.toHaveBeenCalled();
    expect(mocks.clearCurrentChat).toHaveBeenCalledExactlyOnceWith("wt-current");
  });

  it("fences new-chat main-worktree lookup against a later chat", async () => {
    const pending = deferred<{ id: string }>();
    mocks.resolveMainWorktree.mockReturnValueOnce(pending.promise);
    const a = openNewChat({ projectId: "p2" });
    await vi.waitFor(() => expect(mocks.resolveMainWorktree).toHaveBeenCalled());
    await openChat({ id: "b", projectId: "p1" } as never);
    pending.resolve({ id: "main-p2" });
    await a;
    expect(mocks.state.currentProject?.id).toBe("p1");
    expect(mocks.state.activeChatId).toBe("b");
    expect(mocks.clearCurrentChat).not.toHaveBeenCalled();
    expect(mocks.switchWorktreeContext).not.toHaveBeenCalledWith("p2", expect.anything(), expect.anything());
  });

  it("passes a live fence through asynchronous workspace-switch preparation", async () => {
    const pending = deferred<void>();
    let committed = false;
    mocks.switchWorktreeContext.mockImplementationOnce(async (...args: unknown[]) => {
      const options = args[2] as { isCurrent: () => boolean };
      await pending.promise;
      if (options.isCurrent()) committed = true;
    });
    const a = openChat({ id: "a", projectId: "p1" } as never);
    await openNewChat({ projectId: "p1" });
    pending.resolve();
    await a;
    expect(committed).toBe(false);
    expect(mocks.persistActiveChat).not.toHaveBeenCalled();
    expect(mocks.navigateToChat).not.toHaveBeenCalled();
  });
});

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

    expect(mocks.selectProject).toHaveBeenCalledWith(mocks.projects[1], { restoreChat: false, restoreWorkspace: false, isCurrent: expect.any(Function) });
    expect(mocks.switchWorktreeContext).toHaveBeenCalledWith("p2", { id: "wt-9", project_id: "p2" }, { isCurrent: expect.any(Function) });
    expect(mocks.selectChat).toHaveBeenCalledWith(chat, { deferPersist: true });
    expect(mocks.persistActiveChat).toHaveBeenCalledWith("c9");
    expect(mocks.navigateToChat).toHaveBeenCalledWith("c9");
  });

  it("stays in the project when the chat is already in it", async () => {
    await openChat({ id: "c1", projectId: "p1" } as never);
    expect(mocks.selectProject).not.toHaveBeenCalled();
    expect(mocks.switchWorktreeContext).toHaveBeenCalledWith("p1", null, { isCurrent: expect.any(Function) });
  });

  it("refuses a chat whose project is gone", async () => {
    await expect(openChat({ id: "c1", projectId: "gone" } as never)).rejects.toBeInstanceOf(
      ChatProjectUnavailableError,
    );
    expect(mocks.selectChat).toHaveBeenCalledWith(expect.objectContaining({ id: "c1" }), { deferPersist: true });
    expect(mocks.persistActiveChat).not.toHaveBeenCalled();
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
    expect(mocks.selectProject).toHaveBeenCalledWith(mocks.projects[1], { restoreChat: false, restoreWorkspace: false, isCurrent: expect.any(Function) });
    expect(mocks.switchWorktreeContext).toHaveBeenCalledWith("p2", { id: "main-p2" }, { isCurrent: expect.any(Function) });
    expect(mocks.clearCurrentChat).toHaveBeenCalledWith("main-p2");
  });

  it("keeps the current workspace when the project does not change", async () => {
    await openNewChat();
    expect(mocks.selectProject).not.toHaveBeenCalled();
    expect(mocks.clearCurrentChat).toHaveBeenCalledWith("wt-current");
  });
});
