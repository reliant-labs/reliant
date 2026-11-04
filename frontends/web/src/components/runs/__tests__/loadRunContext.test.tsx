import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Chat } from "@/api/client";

const state = vi.hoisted(() => ({
  activeChatId: "initial" as string | null,
  projectId: "project-a",
  getChat: vi.fn(),
  fetchProjects: vi.fn(),
  resolveWorktree: vi.fn(),
  seed: vi.fn(),
  selectProject: vi.fn(),
  persist: vi.fn(),
  navigate: vi.fn(),
  restoreNavigation: vi.fn(),
  switchViewer: vi.fn(),
  saveWorkspace: vi.fn(),
  connect: vi.fn(),
}));

vi.mock("@/hooks/chat-actions", () => ({ getChat: state.getChat }));
vi.mock("@/hooks/chat-queries", () => ({ seedChatDetail: state.seed }));
vi.mock("@/hooks/project-queries", () => ({
  getCachedProjects: () => [], fetchProjects: state.fetchProjects,
}));
vi.mock("@/hooks/worktree-queries", () => ({ resolveWorktree: state.resolveWorktree }));
vi.mock("@/store/chatStore", () => ({ useChatStore: { getState: () => ({
  activeChatId: state.activeChatId,
  selectChat: (chat: Chat) => { state.activeChatId = chat.id; },
  persistActiveChat: state.persist,
}) } }));
vi.mock("@/store/projectStore", () => ({ useProjectStore: { getState: () => ({
  currentProject: { id: state.projectId },
  selectProject: state.selectProject,
}) } }));
vi.mock("@/store/chatNavigationStore", () => ({ useChatNavigationStore: { getState: () => ({
  navigateToChat: state.navigate, restoreFromWorkspaceState: state.restoreNavigation,
}) } }));
vi.mock("@/store/workspaceStateStore", () => ({ useWorkspaceStateStore: { getState: () => ({
  setLastWorktree: state.saveWorkspace,
}) } }));
vi.mock("@/store/globalUpdatesStore", () => ({ useGlobalUpdatesStore: { getState: () => ({ connect: state.connect }) } }));
vi.mock("@/lib/logger", () => ({ logger: { info: vi.fn() } }));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
const run = { id: "run-b", projectId: "project-b", worktreeId: "branch-b" } as Chat;
const chatA = { id: "chat-a", projectId: "project-a" } as Chat;

beforeEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
  state.activeChatId = "initial";
  state.projectId = "project-a";
  state.getChat.mockResolvedValue(run);
  state.fetchProjects.mockResolvedValue([{ id: "project-b" }]);
  state.resolveWorktree.mockResolvedValue({ id: "branch-b", project_id: "project-b" });
  state.selectProject.mockImplementation(async (project) => { state.projectId = project.id; });
});

async function setup() {
  const entered = deferred<void>();
  const commit = deferred<void>();
  // Keep the REAL workspace switch, including its internal async boundary.
  vi.doMock("@/store/viewerStore", async () => {
    entered.resolve();
    await commit.promise;
    return { useViewerStore: { getState: () => ({ switchWorktreeContext: state.switchViewer }) } };
  });
  const { loadRunContext } = await import("../loadRunContext");
  const { openChatInWorkspace } = await import("@/lib/openChatInWorkspace");
  const { useWorktreeStore } = await import("@/store/worktreeStore");
  return { loadRunContext, openChatInWorkspace, useWorktreeStore, entered, commit };
}

function commits() {
  return {
    chat: state.activeChatId, project: state.projectId,
    projects: state.selectProject.mock.calls.slice(),
    seed: state.seed.mock.calls.slice(), persist: state.persist.mock.calls.slice(),
    navigation: state.navigate.mock.calls.slice(), viewers: state.switchViewer.mock.calls.slice(),
    restore: state.restoreNavigation.mock.calls.slice(), saved: state.saveWorkspace.mock.calls.slice(),
  };
}

describe("run navigation fencing", () => {
  it("does not overwrite chat A when an abandoned run lookup resolves", async () => {
    const { loadRunContext, openChatInWorkspace } = await setup();
    const lookup = deferred<Chat>();
    state.getChat.mockReturnValue(lookup.promise);
    const pending = loadRunContext(run.id).catch((error) => error);
    await openChatInWorkspace(chatA, { navigate: true });
    const before = commits();
    lookup.resolve(run);
    expect(await pending).toMatchObject({ name: "AbortError" });
    expect(commits()).toEqual(before);
  });

  it.each([
    ["projects", false], ["worktree", false], ["commit", false],
    ["projects", true], ["worktree", true], ["commit", true],
  ] as const)("fences mutations at %s (superseding navigation: %s)", async (stage, supersede) => {
    const { loadRunContext, openChatInWorkspace, entered, commit, useWorktreeStore } = await setup();
    const reached = deferred<void>();
    const projects = deferred<{ id: string }[]>();
    const worktree = deferred<{ id: string; project_id: string }>();
    if (stage === "projects") state.fetchProjects.mockImplementation(() => { reached.resolve(); return projects.promise; });
    if (stage === "worktree") state.resolveWorktree.mockImplementation(() => { reached.resolve(); return worktree.promise; });
    const controller = new AbortController();
    const pending = loadRunContext(run.id, controller.signal).catch((error) => error);
    await (stage === "commit" ? entered.promise : reached.promise);
    if (supersede) {
      // Avoid holding A behind B's controlled project request.
      state.projectId = "project-a";
      await openChatInWorkspace(chatA, { navigate: true });
    } else {
      controller.abort();
    }
    const before = commits();
    projects.resolve([{ id: "project-b" }]);
    worktree.resolve({ id: "branch-b", project_id: "project-b" });
    commit.resolve();
    expect(await pending).toMatchObject({ name: "AbortError" });
    expect(commits()).toEqual(before);
    expect(useWorktreeStore.getState().currentWorktreeId).toBeNull();
  });

  it("unmount cancels the route's lookup, not just its React updates", async () => {
    await setup();
    const lookup = deferred<Chat>();
    state.getChat.mockReturnValue(lookup.promise);
    const { useRunRoute } = await import("../RunRouteLoader");
    const { unmount } = renderHook(() => useRunRoute(run.id));
    unmount();
    await act(async () => { lookup.resolve(run); await lookup.promise; });
    expect(state.seed).not.toHaveBeenCalled();
    expect(state.selectProject).not.toHaveBeenCalled();
    expect(state.activeChatId).toBe("initial");
    expect(state.connect).not.toHaveBeenCalled();
  });

  it("still commits a current run and connects its transcript", async () => {
    const { commit } = await setup();
    commit.resolve();
    const { useRunRoute } = await import("../RunRouteLoader");
    const { result } = renderHook(() => useRunRoute(run.id));
    await act(async () => { await state.getChat.mock.results[0].value; });
    expect(result.current).toMatchObject({ status: "ready", chat: run });
    expect(state.projectId).toBe("project-b");
    expect(state.switchViewer).toHaveBeenCalledWith("project-b", "branch-b");
    expect(state.persist).toHaveBeenCalledWith(run.id);
    expect(state.navigate).toHaveBeenCalledWith(run.id);
    expect(state.connect).toHaveBeenCalledOnce();
  });
});
