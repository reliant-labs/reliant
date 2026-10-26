/**
 * The project store in the global window: the context project and the
 * sidebar filter are separate, and there is no project picker to fall back
 * to — a window with projects always lands on one.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  projects: [] as Array<{ id: string; name: string; path: string; last_active: string }>,
  loadChats: vi.fn(async () => {}),
  clearCurrentChat: vi.fn(),
  deleteProjectRpc: vi.fn(async (_id: string) => {}),
}));

vi.mock("../../hooks/project-queries", () => ({
  createProject: vi.fn(),
  deleteProject: (id: string) => mocks.deleteProjectRpc(id),
  fetchProjects: async () => mocks.projects,
  getCachedProjects: () => mocks.projects,
  refreshProject: vi.fn(),
  sameProject: () => true,
  subscribeToProjectRows: () => () => {},
  touchProject: vi.fn(),
  updateProject: vi.fn(),
}));
vi.mock("../chatStore", () => ({
  useChatStore: {
    getState: () => ({ loadChats: mocks.loadChats, clearCurrentChat: mocks.clearCurrentChat, selectChat: vi.fn() }),
  },
}));
vi.mock("../worktreeStore", () => ({
  useWorktreeStore: { getState: () => ({ reset: vi.fn(), currentWorktreeId: null, switchWorktreeContext: vi.fn() }) },
}));
vi.mock("../viewerStore", () => ({
  useViewerStore: {
    getState: () => ({ clearViewersForProjectSwitch: vi.fn(), restoreFromWorkspaceState: vi.fn() }),
  },
}));
vi.mock("../chatNavigationStore", () => ({
  useChatNavigationStore: { getState: () => ({ restoreFromWorkspaceState: vi.fn() }) },
}));
vi.mock("../../hooks/worktree-actions", () => ({ restoreLastWorktree: vi.fn(async () => {}) }));
vi.mock("../../hooks/worktree-queries", () => ({ resolveWorktree: vi.fn() }));
vi.mock("../../hooks/chat-queries", () => ({ resolveChat: vi.fn() }));
vi.mock("../../lib/analytics", () => ({ trackEvent: vi.fn() }));

import { mostRecentProject, useProjectStore } from "../projectStore";
import { useWorkspaceStateStore } from "../workspaceStateStore";

const project = (id: string, last_active: string) => ({ id, name: id, path: `/p/${id}`, last_active });

beforeEach(() => {
  vi.clearAllMocks();
  mocks.projects = [project("old", "2025-01-01T00:00:00Z"), project("new", "2025-06-01T00:00:00Z")];
  useProjectStore.setState({ currentProject: null });
  useWorkspaceStateStore.setState({ lastProjectId: null, projectFilterId: null, projects: {} });
});

describe("mostRecentProject", () => {
  it("picks the most recently used project", () => {
    expect(mostRecentProject(mocks.projects as never)?.id).toBe("new");
    expect(mostRecentProject([])).toBeUndefined();
  });
});

describe("restoreLastProject", () => {
  it("restores the last project used", async () => {
    useWorkspaceStateStore.setState({ lastProjectId: "old" });
    await expect(useProjectStore.getState().restoreLastProject()).resolves.toBe(true);
    expect(useProjectStore.getState().currentProject?.id).toBe("old");
  });

  it("lands on the most recent project when there is no last one — never on a picker", async () => {
    await expect(useProjectStore.getState().restoreLastProject()).resolves.toBe(true);
    expect(useProjectStore.getState().currentProject?.id).toBe("new");
  });

  it("drops a last project that is gone and lands on the most recent", async () => {
    useWorkspaceStateStore.setState({ lastProjectId: "deleted" });
    await useProjectStore.getState().restoreLastProject();
    expect(useProjectStore.getState().currentProject?.id).toBe("new");
  });

  it("reports nothing to restore with no projects at all (the first-run state)", async () => {
    mocks.projects = [];
    await expect(useProjectStore.getState().restoreLastProject()).resolves.toBe(false);
    expect(useProjectStore.getState().currentProject).toBeNull();
  });
});

describe("setProjectFilter", () => {
  it("filters to a project, moves the context into it, and reloads the list for the new scope", async () => {
    useProjectStore.setState({ currentProject: mocks.projects[0] as never });
    await useProjectStore.getState().setProjectFilter("new");

    expect(useWorkspaceStateStore.getState().projectFilterId).toBe("new");
    expect(useProjectStore.getState().currentProject?.id).toBe("new");
    expect(mocks.loadChats).toHaveBeenCalled();
  });

  it("All projects leaves the context where it is", async () => {
    useProjectStore.setState({ currentProject: mocks.projects[0] as never });
    useWorkspaceStateStore.setState({ projectFilterId: "old" });
    await useProjectStore.getState().setProjectFilter(null);

    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
    expect(useProjectStore.getState().currentProject?.id).toBe("old");
  });

  it("falls back to All projects for a project that is gone", async () => {
    await useProjectStore.getState().setProjectFilter("deleted");
    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
  });
});

describe("selectProject", () => {
  it("opens the new project's own chat view on a switch, unless the caller opens a chat itself", async () => {
    useProjectStore.setState({ currentProject: mocks.projects[0] as never });
    await useProjectStore.getState().selectProject(mocks.projects[1] as never);
    expect(mocks.clearCurrentChat).toHaveBeenCalledTimes(1);

    mocks.clearCurrentChat.mockClear();
    await useProjectStore.getState().selectProject(mocks.projects[0] as never, { restoreChat: false });
    expect(mocks.clearCurrentChat).not.toHaveBeenCalled();
  });

  it("never reloads the chat list: it spans projects", async () => {
    await useProjectStore.getState().selectProject(mocks.projects[1] as never);
    expect(mocks.loadChats).not.toHaveBeenCalled();
  });
});

describe("deleteProject", () => {
  it("moves the context to the most recent remaining project and drops a filter on the deleted one", async () => {
    useProjectStore.setState({ currentProject: mocks.projects[1] as never });
    useWorkspaceStateStore.setState({ projectFilterId: "new" });
    mocks.deleteProjectRpc.mockImplementation(async () => {
      mocks.projects = mocks.projects.filter((p) => p.id !== "new");
    });

    await useProjectStore.getState().deleteProject("new");

    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
    expect(useProjectStore.getState().currentProject?.id).toBe("old");
  });
});
