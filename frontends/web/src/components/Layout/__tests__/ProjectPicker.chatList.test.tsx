/**
 * Picking a project in the sidebar's project picker shows that project's
 * chats, from any route — including from another project's page.
 *
 * The regression this guards (prod, v1.8.5 → v1.8.12): on `/project/A`,
 * picking B sent TouchProject for B and then for A again, the URL stayed on
 * `/project/A`, and the list kept showing A's chats. The URL bridge
 * (ProjectFilterRouteSync) read the pathname from the router's location, which
 * moves the moment a navigation starts, and the project id from the route
 * params, which follow only once the new match resolves. Mid-navigation it saw
 * `/project/B` with the param still `A`, took that for a request to filter to
 * A, and undid the pick. From `/` the stale param was undefined, which the
 * bridge skips, so only a project → project switch broke.
 *
 * The real router, stores, query hooks and picker run here; only the
 * transport is fake, so the assertion is on the ListChats request the server
 * would see and the chats the list renders.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

import { ProjectFilterRouteSync } from "../ProjectFilterRouteSync";
import { ProjectFilterMenu } from "../ProjectFilterMenu";
import { useChatListWithWorkspaces } from "../../../hooks/chat-queries";
import type { Project } from "../../../hooks/project-queries";
import { setTransport } from "../../../lib/connect";
import { queryClient } from "../../../lib/query-client";
import { mockTransport } from "../../../lib/test-utils";
import { useProjectStore } from "../../../store/projectStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";

const project = (id: string): Project =>
  ({ id, name: `Project ${id.toUpperCase()}`, path: `/p/${id}`, last_active: "", is_git_repo: false, is_forge: false }) as Project;
const projects = [project("a"), project("b"), project("c")];

const touchProject = vi.hoisted(() => vi.fn());
vi.mock("../../../hooks/project-queries", async (original) => ({
  ...(await original<typeof import("../../../hooks/project-queries")>()),
  getCachedProjects: () => projects,
  fetchProjects: async () => projects,
  useProjects: () => ({ projects }),
  touchProject,
}));

/** Every ListChats request's project id ("" is All projects). */
let listChatsProjectIds: string[] = [];

function chatsOf(projectId: string) {
  const ids = projectId ? [projectId] : projects.map((p) => p.id);
  return ids.map((id) => ({ id: `chat-${id}`, title: `Chat in ${id.toUpperCase()}`, projectId: id }));
}

/** The sidebar's wiring, minus its layout: the picker, the list it scopes. */
function ChatListShell() {
  const filterId = useWorkspaceStateStore((state) => state.projectFilterId);
  const { data } = useChatListWithWorkspaces(filterId);
  return (
    <>
      <ProjectFilterMenu
        filterId={filterId}
        projects={projects}
        onChange={(id) => void useProjectStore.getState().setProjectFilter(id)}
        onAddProject={() => {}}
        onManageProjects={() => {}}
      />
      <ul data-testid="chat-list">
        {(data?.chats ?? []).map((chat) => (
          <li key={chat.id}>{chat.title}</li>
        ))}
      </ul>
    </>
  );
}

async function mountAt(path: string) {
  const root = createRootRoute({
    component: () => (
      <>
        <ProjectFilterRouteSync />
        <Outlet />
      </>
    ),
  });
  const all = createRoute({ getParentRoute: () => root, path: "/", component: ChatListShell });
  const filtered = createRoute({ getParentRoute: () => root, path: "/project/$projectId", component: ChatListShell });
  const router = createRouter({
    routeTree: root.addChildren([all, filtered]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = router;
  await router.load();
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

async function pick(testId: string) {
  await userEvent.click(screen.getByTestId("project-filter-trigger"));
  await userEvent.click(screen.getByTestId(testId));
}

const listed = () => within(screen.getByTestId("chat-list")).queryAllByRole("listitem").map((li) => li.textContent);

beforeEach(() => {
  queryClient.clear();
  listChatsProjectIds = [];
  touchProject.mockClear();
  setTransport(
    mockTransport({
      "ChatService.ListChats": (req: { projectId: string }) => {
        listChatsProjectIds.push(req.projectId);
        return { chats: chatsOf(req.projectId) };
      },
    }),
  );
  useProjectStore.setState({ currentProject: null });
  useWorkspaceStateStore.setState({ projectFilterId: null });
});

afterEach(() => {
  cleanup();
  setTransport(null);
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe("the project picker scopes the chat list", () => {
  it("switching from one project's page to another lists the picked project's chats", async () => {
    const router = await mountAt("/project/a");
    await waitFor(() => expect(listed()).toEqual(["Chat in A"]));
    await waitFor(() => expect(useProjectStore.getState().currentProject?.id).toBe("a"));
    touchProject.mockClear();

    await act(async () => {
      await pick("project-filter-b");
    });

    await waitFor(() => expect(listed()).toEqual(["Chat in B"]));
    expect(listChatsProjectIds).toContain("b");
    // Nothing reverted the pick: the filter, the context project and the URL
    // all say B, and A was never touched again.
    expect(useWorkspaceStateStore.getState().projectFilterId).toBe("b");
    expect(useProjectStore.getState().currentProject?.id).toBe("b");
    expect(router.state.location.pathname).toBe("/project/b");
    expect(touchProject.mock.calls.map(([id]) => id)).not.toContain("a");
  });

  it("keeps the pick through several switches, then All projects", async () => {
    const router = await mountAt("/project/a");
    await waitFor(() => expect(listed()).toEqual(["Chat in A"]));

    await act(async () => {
      await pick("project-filter-c");
    });
    await waitFor(() => expect(listed()).toEqual(["Chat in C"]));

    await act(async () => {
      await pick("project-filter-b");
    });
    await waitFor(() => expect(listed()).toEqual(["Chat in B"]));
    expect(router.state.location.pathname).toBe("/project/b");

    await act(async () => {
      await pick("project-filter-all");
    });
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in B", "Chat in C"]));
    expect(router.state.location.pathname).toBe("/");
    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
  });

  it("switching from All projects lists the picked project's chats", async () => {
    const router = await mountAt("/");
    await waitFor(() => expect(listed()).toHaveLength(3));

    await act(async () => {
      await pick("project-filter-b");
    });

    await waitFor(() => expect(listed()).toEqual(["Chat in B"]));
    expect(router.state.location.pathname).toBe("/project/b");
  });
});
