import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { ProjectFilterRouteSync, useRouteProjectId } from "./ProjectFilterRouteSync";
import { useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { useChatStore } from "../../store/chatStore";

vi.mock("../../hooks/project-queries", async (original) => ({
  ...(await original<typeof import("../../hooks/project-queries")>()),
  getCachedProjects: () => [{ id: "a", name: "A", path: "/a" }],
  fetchProjects: async () => [{ id: "a", name: "A", path: "/a" }], touchProject: vi.fn(),
}));

beforeEach(() => {
  useWorkspaceStateStore.setState({ projectFilterId: null });
  useProjectStore.setState({ currentProject: { id: "a", name: "A", path: "/a" } as never });
  vi.spyOn(useChatStore.getState(), "loadChats").mockResolvedValue();
});
afterEach(() => {
  cleanup();
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

async function mountShell() {
  function StartupProbe() {
    const routeProjectId = useRouteProjectId();
    return <output data-testid="startup-skip">{String(Boolean(routeProjectId))}</output>;
  }
  const root = createRootRoute({ component: () => <><StartupProbe /><ProjectFilterRouteSync /><Outlet /></> });
  const all = createRoute({ getParentRoute: () => root, path: "/", component: () => <div>All</div> });
  const filtered = createRoute({ getParentRoute: () => root, path: "/project/$projectId", component: () => <div>Project</div> });
  const history = createMemoryHistory({ initialEntries: ["/"] });
  const router = createRouter({ routeTree: root.addChildren([all, filtered]), history });
  (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = router;
  await router.load();
  render(<RouterProvider router={router} />);
  await waitFor(() => expect(router.state.status).toBe("idle"));
  return { router, history };
}

it("mounted shell honors Back to All without pushing the filtered URL again", async () => {
  const { router, history } = await mountShell();
  expect(screen.getByTestId("startup-skip").textContent).toBe("false");
  await act(async () => { await useProjectStore.getState().setProjectFilter("a", { restoreWorkspace: false }); });
  await waitFor(() => expect(router.state.location.pathname).toBe("/project/a"));
  const pushes = vi.spyOn(history, "push");
  await act(async () => { history.back(); });
  await waitFor(() => expect(router.state.location.pathname).toBe("/"));
  await waitFor(() => expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull());
  expect(screen.getByTestId("startup-skip").textContent).toBe("false");
  expect(pushes).not.toHaveBeenCalled();
  await act(async () => { history.forward(); });
  await waitFor(() => expect(router.state.location.pathname).toBe("/project/a"));
  await act(async () => { history.back(); });
  await waitFor(() => expect(router.state.location.pathname).toBe("/"));
  expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
});

it("restores a persisted filter only on the initial root", async () => {
  useWorkspaceStateStore.setState({ projectFilterId: "a" });
  const { router } = await mountShell();
  await waitFor(() => expect(router.state.location.pathname).toBe("/project/a"));
  expect(screen.getByTestId("startup-skip").textContent).toBe("true");
});
