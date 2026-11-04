import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Chat } from "../../api/client";

const state = vi.hoisted(() => ({
  activeChatId: null as string | null,
  projectId: "project-1",
  persist: vi.fn(),
  navigate: vi.fn(),
  restoreNavigation: vi.fn(),
  switchViewer: vi.fn(),
  saveWorkspace: vi.fn(),
  resolveWorktree: vi.fn(async (id: string) => ({ id, project_id: "project-1" })),
}));

vi.mock("../../store/chatStore", () => ({
  useChatStore: {
    getState: () => ({
      activeChatId: state.activeChatId,
      selectChat: (chat: Chat) => { state.activeChatId = chat.id; },
      persistActiveChat: state.persist,
    }),
  },
}));
vi.mock("../../store/projectStore", () => ({
  useProjectStore: { getState: () => ({ currentProject: { id: state.projectId } }) },
}));
vi.mock("../../store/chatNavigationStore", () => ({
  useChatNavigationStore: {
    getState: () => ({ navigateToChat: state.navigate, restoreFromWorkspaceState: state.restoreNavigation }),
  },
}));
vi.mock("../../store/workspaceStateStore", () => ({
  useWorkspaceStateStore: { getState: () => ({ setLastWorktree: state.saveWorkspace }) },
}));
vi.mock("../../hooks/worktree-queries", () => ({ resolveWorktree: state.resolveWorktree }));
vi.mock("../logger", () => ({ logger: { info: vi.fn() } }));

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

function chat(id: string, worktreeId?: string): Chat {
  return { id, worktreeId } as Chat;
}

// Use the REAL switch, suspending its internal dynamic import rather than
// mocking switchWorktreeContext as an immediately completed operation.
async function setup() {
  const entered = deferred();
  const gate = deferred();
  vi.doMock("../../store/viewerStore", async () => {
    entered.resolve();
    await gate.promise;
    return { useViewerStore: { getState: () => ({ switchWorktreeContext: state.switchViewer }) } };
  });
  const { useWorktreeStore } = await import("../../store/worktreeStore");
  const { openChatInWorkspace } = await import("../openChatInWorkspace");
  return { useWorktreeStore, openChatInWorkspace, entered, gate };
}

beforeEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
  state.activeChatId = null;
  state.projectId = "project-1";
});

describe("chat open workspace commit fence", () => {
  it("keeps main chat B and its tool-routing workspace when branch A's switch resumes", async () => {
    const { openChatInWorkspace, useWorktreeStore, entered, gate } = await setup();
    const a = openChatInWorkspace(chat("a", "branch-a"), { navigate: true });
    await entered.promise;
    await openChatInWorkspace(chat("b"), { navigate: true });
    gate.resolve();
    await a;

    expect(state.activeChatId).toBe("b");
    expect(useWorktreeStore.getState().currentWorktreeId).toBeNull();
    expect(state.switchViewer).not.toHaveBeenCalled();
    expect(state.restoreNavigation).not.toHaveBeenCalled();
    expect(state.saveWorkspace).not.toHaveBeenCalled();
    expect(state.persist.mock.calls).toEqual([["b"]]);
    expect(state.navigate.mock.calls).toEqual([["b"]]);
  });

  it("does not commit an old project's workspace even if the chat identity stays the same", async () => {
    const { openChatInWorkspace, useWorktreeStore, entered, gate } = await setup();
    const a = openChatInWorkspace(chat("a", "branch-a"), { navigate: true });
    await entered.promise;
    state.projectId = "project-2";
    useWorktreeStore.getState().selectWorktree({ id: "branch-b", project_id: "project-2" });
    state.saveWorkspace.mockClear();
    gate.resolve();
    await a;

    expect(useWorktreeStore.getState().currentWorktreeId).toBe("branch-b");
    expect(useWorktreeStore.getState().currentWorktreeProjectId).toBe("project-2");
    expect(state.switchViewer).not.toHaveBeenCalled();
    expect(state.restoreNavigation).not.toHaveBeenCalled();
    expect(state.saveWorkspace).not.toHaveBeenCalled();
    expect(state.persist).not.toHaveBeenCalled();
    expect(state.navigate).not.toHaveBeenCalled();
  });

  it("does not write state after the chat view was cleared during the switch", async () => {
    const { openChatInWorkspace, useWorktreeStore, entered, gate } = await setup();
    const a = openChatInWorkspace(chat("a", "branch-a"), { navigate: true });
    await entered.promise;
    state.activeChatId = null;
    gate.resolve();
    await a;

    expect(useWorktreeStore.getState().currentWorktreeId).toBeNull();
    expect(state.switchViewer).not.toHaveBeenCalled();
    expect(state.restoreNavigation).not.toHaveBeenCalled();
    expect(state.persist).not.toHaveBeenCalled();
    expect(state.navigate).not.toHaveBeenCalled();
  });

  it("allows only the fresh A open after multiple superseded requests, including A-to-B-to-A", async () => {
    const { openChatInWorkspace, useWorktreeStore, entered, gate } = await setup();
    const oldA = openChatInWorkspace(chat("a", "branch-a"), { navigate: true });
    await entered.promise;
    const oldC = openChatInWorkspace(chat("c", "branch-c"), { navigate: true });
    await openChatInWorkspace(chat("b"), { navigate: true });
    // Old A asks to navigate, fresh A does not: identity-only checks would
    // wrongly let old A restore the queue and navigate once it resumes.
    const freshA = openChatInWorkspace(chat("a", "branch-a"));
    gate.resolve();
    await Promise.all([oldA, oldC, freshA]);

    expect(state.activeChatId).toBe("a");
    expect(useWorktreeStore.getState().currentWorktreeId).toBe("branch-a");
    expect(state.switchViewer.mock.calls).toEqual([["project-1", "branch-a"]]);
    expect(state.restoreNavigation.mock.calls).toEqual([["project-1", "branch-a"]]);
    expect(state.saveWorkspace.mock.calls).toEqual([["project-1", "branch-a"]]);
    expect(state.persist.mock.calls).toEqual([["b"], ["a"]]);
    expect(state.navigate.mock.calls).toEqual([["b"]]);
  });

  it("commits a current fresh branch open and can subsequently return to main", async () => {
    const { openChatInWorkspace, useWorktreeStore, entered, gate } = await setup();
    const a = openChatInWorkspace(chat("a", "branch-a"), { navigate: true });
    await entered.promise;
    gate.resolve();
    await a;
    expect(useWorktreeStore.getState().currentWorktreeId).toBe("branch-a");
    await openChatInWorkspace(chat("b"), { navigate: true });

    expect(state.activeChatId).toBe("b");
    expect(useWorktreeStore.getState().currentWorktreeId).toBeNull();
    expect(state.switchViewer.mock.calls).toEqual([["project-1", "branch-a"], ["project-1", null]]);
    expect(state.restoreNavigation.mock.calls).toEqual([["project-1", "branch-a"], ["project-1", null]]);
    expect(state.persist.mock.calls).toEqual([["a"], ["b"]]);
    expect(state.navigate.mock.calls).toEqual([["a"], ["b"]]);
  });
});
