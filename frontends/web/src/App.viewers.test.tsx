import { act, render, screen } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FileNode } from "./components/FileBrowser";

vi.mock("./ModernApp", () => ({ default: () => <div>Chat app</div> }));
vi.mock("./hooks/project-queries", () => ({
  getCachedProjects: () => [], fetchProjects: vi.fn(), touchProject: vi.fn(),
  subscribeToProjectRows: () => () => {},
}));
vi.mock("./store/chatStore", () => ({ useChatStore: { getState: () => ({ clearCurrentChat: vi.fn() }) } }));
vi.mock("./store/chatNavigationStore", () => ({ useChatNavigationStore: { getState: () => ({ restoreFromWorkspaceState: vi.fn() }) } }));
vi.mock("./hooks/worktree-actions", () => ({ restoreLastWorktree: vi.fn() }));
vi.mock("./lib/analytics", () => ({ trackEvent: vi.fn() }));

import App from "./App";
import { useViewerStore } from "./store/viewerStore";
import { useProjectStore } from "./store/projectStore";
import { useWorktreeStore } from "./store/worktreeStore";
import { useWorkspaceStateStore } from "./store/workspaceStateStore";

const project = (id: string) => ({ id, name: id, path: `/projects/${id}`, is_git_repo: true });
const file = { name: "readme.md", path: "/projects/a/readme.md", type: "file" } as FileNode;

beforeEach(() => {
  useWorkspaceStateStore.setState({ projects: {}, lastProjectId: null });
  useProjectStore.setState({ currentProject: project("a") as never });
  useWorktreeStore.getState().reset();
  useViewerStore.setState({ viewers: [], activeViewerId: null, closedFiles: [] });
});

describe("chat route viewer lifecycle", () => {
  it.each(["runs", "settings"] as const)("preserves file tabs and active selection on a %s round-trip", async (route) => {
    const root = createRootRoute({ component: () => <Outlet /> });
    const chat = createRoute({ getParentRoute: () => root, path: "/chat", component: App });
    const global = createRoute({ getParentRoute: () => root, path: `/${route}`, component: () => <div>Global page</div> });
    const router = createRouter({
      routeTree: root.addChildren([chat, global]),
      history: createMemoryHistory({ initialEntries: ["/chat"] }),
    });
    render(<RouterProvider router={router} />);
    await screen.findByText("Chat app");
    act(() => { useViewerStore.getState().openFileViewer(file, "a"); });
    const { viewers, activeViewerId } = useViewerStore.getState();
    await act(async () => { await router.navigate({ to: `/${route}` }); });
    expect(screen.queryByText("Chat app")).not.toBeInTheDocument();
    await act(async () => { await router.navigate({ to: "/chat" }); });
    expect(screen.getByText("Chat app")).toBeInTheDocument();
    expect(useViewerStore.getState().viewers).toEqual(viewers);
    expect(useViewerStore.getState().activeViewerId).toBe(activeViewerId);
  });

  it("still clears tabs for a genuine project selection", async () => {
    useViewerStore.getState().openFileViewer(file, "a");
    await useProjectStore.getState().selectProject(project("b") as never, { restoreWorkspace: false });
    expect(useProjectStore.getState().currentProject?.id).toBe("b");
    expect(useViewerStore.getState().viewers).toEqual([]);
    expect(useViewerStore.getState().activeViewerId).toBeNull();
  });

  it("switches workspace tabs and restores them when returning to the original workspace", async () => {
    useViewerStore.getState().openFileViewer(file, "a");
    const original = useViewerStore.getState().serializeViewers();
    await useWorktreeStore.getState().switchWorktreeContext("a", { id: "branch", project_id: "a" });
    expect(useViewerStore.getState().viewers).toEqual([]);
    expect(useViewerStore.getState().activeViewerId).toBeNull();
    await useWorktreeStore.getState().switchWorktreeContext("a", null);
    expect(useViewerStore.getState().serializeViewers()).toEqual(original);
    expect(useViewerStore.getState().activeViewerId).toBe(useViewerStore.getState().viewers[0].id);
  });
});
