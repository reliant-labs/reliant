/**
 * The chat list's project filter, choose N of M (hooks/useChatProjectFilter,
 * Projects/ChatProjectFilter), scopes the list from any route.
 *
 *   - Two of five projects list only their chats, in one ListChats naming
 *     both (`project_ids`), at `/?projects=`.
 *   - The choice survives a refresh: a new page at that URL lists the same.
 *   - Clearing it lists every project's chats again.
 *   - One project is still the single-project filter old builds read:
 *     `/project/<id>` and workspaceState's projectFilterId, and a persisted
 *     one still loads.
 *   - A new tab on the bare home starts with the remembered choice.
 *
 * The regression the single-project half guards (prod, v1.8.5 → v1.8.12): on
 * `/project/A`, picking B sent TouchProject for B and then for A again, the
 * URL stayed on `/project/A`, and the list kept showing A's chats. The URL
 * bridge (ProjectFilterRouteSync) read the pathname from the router's
 * location, which moves the moment a navigation starts, and the project id
 * from the route params, which follow only once the new match resolves.
 *
 * The real router, stores, query hooks and selector run here; only the
 * transport (and the settings read) is fake, so the assertions are on the
 * ListChats requests the server would see and the chats the list renders.
 * The list is mounted once, outside the routes, as the shell mounts the
 * sidebar.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useLocation,
} from "@tanstack/react-router";

import type { ListChatsRequest } from "../../../gen/reliant/v1/chat_pb";
import { ProjectFilterRouteSync } from "../ProjectFilterRouteSync";
import { ChatProjectFilterButton, ChatProjectFilterScope } from "../../Projects/ChatProjectFilter";
import { useChatListWithWorkspaces } from "../../../hooks/chat-queries";
import type { Project } from "../../../hooks/project-queries";
import { resetChatProjectFilterForTests, useChatProjectFilter } from "../../../hooks/useChatProjectFilter";
import { setTransport } from "../../../lib/connect";
import { queryClient } from "../../../lib/query-client";
import { mockTransport } from "../../../lib/test-utils";
import { indexSearchSchema } from "../../../routeSchemas";
import { useProjectStore } from "../../../store/projectStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";

const project = (id: string): Project =>
  ({ id, name: `Project ${id.toUpperCase()}`, path: `/p/${id}`, last_active: "", is_git_repo: false, is_forge: false }) as Project;
const projects = ["a", "b", "c", "d", "e"].map(project);

const touchProject = vi.hoisted(() => vi.fn());
vi.mock("../../../hooks/project-queries", async (original) => ({
  ...(await original<typeof import("../../../hooks/project-queries")>()),
  getCachedProjects: () => projects,
  fetchProjects: async () => projects,
  useProjects: () => ({ projects, isLoading: false, error: null, refetch: () => {} }),
  touchProject,
}));

// The selector reads the catalog; with none, it lists the plain projects (its fallback).
vi.mock("@/hooks/project-catalog-queries", () => ({
  useProjectCatalog: () => ({ catalog: undefined, isLoading: false, error: new Error("no catalog here"), refetch: () => {} }),
}));
/** The person's remembered filter (sidebar.projects `filter`), and what was written to it. */
const remembered = vi.hoisted(() => ({ filter: [] as string[], writes: [] as string[][] }));
vi.mock("@/hooks/sidebar-project-queries", () => ({
  useSidebarProjectPrefs: () => ({
    prefs: { pinned: new Set(), hidden: new Set(), showAll: false, filter: remembered.filter },
    writable: true,
    loadFailed: false,
    setFilter: (ids: string[]) => {
      remembered.writes.push([...ids]);
    },
  }),
}));
vi.mock("@/components/Apps/startBuilding", () => ({ useStartBuildingApp: () => () => {} }));

/** Every ListChats request: its project_id and project_ids. */
let requests: { projectId: string; projectIds: string[] }[] = [];

/** The server's answer: the union of project_id and project_ids, or every project. */
function chatsFor(req: { projectId: string; projectIds: string[] }) {
  const named = [...(req.projectId ? [req.projectId] : []), ...req.projectIds];
  const ids = named.length > 0 ? named : projects.map((p) => p.id);
  return ids.map((id) => ({ id: `chat-${id}`, title: `Chat in ${id.toUpperCase()}`, projectId: id }));
}

/** The sidebar's wiring, minus its layout: the filter, its summary, the list it scopes. */
function ChatListShell() {
  const filterId = useWorkspaceStateStore((state) => state.projectFilterId);
  const location = useLocation();
  const filter = useChatProjectFilter({ sync: { pathname: location.pathname, search: location.search } });
  const { data } = useChatListWithWorkspaces(filter.mode === "many" ? filter.ids : filterId);
  return (
    <>
      <ChatProjectFilterButton filter={filter} />
      <ChatProjectFilterScope filter={filter} />
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
        <ChatListShell />
        <Outlet />
      </>
    ),
  });
  const page = () => null;
  const all = createRoute({ getParentRoute: () => root, path: "/", component: page, validateSearch: indexSearchSchema });
  const filtered = createRoute({ getParentRoute: () => root, path: "/project/$projectId", component: page });
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

/** Open the filter and act in the selector. */
async function inFilter(act: () => Promise<void>) {
  await userEvent.click(screen.getByTestId("chat-project-filter-trigger"));
  await screen.findByTestId("project-selector");
  await act();
  await userEvent.click(screen.getByTestId("project-selector-done"));
}
const toggle = (id: string) => userEvent.click(screen.getByTestId(`project-selector-row-${id}`));
/** Only this, from the keyboard: search the project, then Shift+Enter on it. */
const onlyThis = async (id: string) => {
  const search = screen.getByTestId("project-selector-search");
  fireEvent.change(search, { target: { value: `Project ${id.toUpperCase()}` } });
  await screen.findByTestId(`project-selector-row-${id}`);
  fireEvent.keyDown(search, { key: "Enter", shiftKey: true });
};

const listed = () => within(screen.getByTestId("chat-list")).queryAllByRole("listitem").map((li) => li.textContent);
const ALL = ["Chat in A", "Chat in B", "Chat in C", "Chat in D", "Chat in E"];

/** A fresh page: no cache, no in-memory filter, the persisted single filter kept. */
function newPage() {
  cleanup();
  queryClient.clear();
  resetChatProjectFilterForTests();
}

beforeEach(() => {
  newPage();
  requests = [];
  remembered.filter = [];
  remembered.writes = [];
  touchProject.mockClear();
  setTransport(
    mockTransport({
      "ChatService.ListChats": (req: unknown) => {
        const { projectId, projectIds } = req as ListChatsRequest;
        requests.push({ projectId, projectIds: [...projectIds] });
        return { chats: chatsFor({ projectId, projectIds: [...projectIds] }) };
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

describe("the chat list's project filter, choose N of M", () => {
  it("lists only the chats of the two projects of five chosen, in one ListChats naming both", async () => {
    const router = await mountAt("/");
    await waitFor(() => expect(listed()).toEqual(ALL));

    await act(async () => {
      await inFilter(async () => {
        await toggle("b");
        await toggle("d");
      });
    });

    await waitFor(() => expect(listed()).toEqual(["Chat in B", "Chat in D"]));
    expect(requests.at(-1)?.projectIds.sort()).toEqual(["b", "d"]);
    expect(router.state.location.pathname).toBe("/");
    expect(router.state.location.search).toMatchObject({ projects: "b,d" });
    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
    expect(screen.getByTestId("project-filter-label")).toHaveTextContent("2 projects");
    // Remembered for the next tab.
    expect(remembered.writes.at(-1)).toEqual(["b", "d"]);
  });

  it("keeps the choice through a refresh: the URL is the choice", async () => {
    await mountAt("/?projects=a,e");
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in E"]));

    newPage();
    await mountAt("/?projects=a,e");
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in E"]));
    expect(screen.getByTestId("project-filter-label")).toHaveTextContent("2 projects");
  });

  it("clearing it lists every project's chats again, and forgets the choice", async () => {
    const router = await mountAt("/?projects=a,c");
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in C"]));

    await act(async () => {
      await userEvent.click(screen.getByTestId("project-filter-clear"));
    });

    await waitFor(() => expect(listed()).toEqual(ALL));
    expect(router.state.location.search).not.toHaveProperty("projects");
    expect(screen.queryByTestId("project-filter-label")).toBeNull();
    expect(remembered.writes.at(-1)).toEqual([]);
  });

  it("one project is the single-project filter: /project/<id>, and the old setting still loads", async () => {
    // A persisted single filter (what old builds write) at the bare home.
    useWorkspaceStateStore.setState({ projectFilterId: "c" });
    const router = await mountAt("/");
    await waitFor(() => expect(router.state.location.pathname).toBe("/project/c"));
    await waitFor(() => expect(listed()).toEqual(["Chat in C"]));
    expect(requests.at(-1)).toEqual({ projectId: "c", projectIds: [] });

    // Only this: another single project, by the old route.
    await act(async () => {
      await inFilter(() => onlyThis("b"));
    });
    await waitFor(() => expect(listed()).toEqual(["Chat in B"]));
    expect(router.state.location.pathname).toBe("/project/b");
    expect(useWorkspaceStateStore.getState().projectFilterId).toBe("b");
    expect(useProjectStore.getState().currentProject?.id).toBe("b");
    expect(touchProject.mock.calls.map(([id]) => id)).not.toContain("c");
  });

  it("goes from one project to several and back to all", async () => {
    const router = await mountAt("/project/a");
    await waitFor(() => expect(listed()).toEqual(["Chat in A"]));

    await act(async () => {
      await inFilter(() => toggle("e"));
    });
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in E"]));
    expect(router.state.location.pathname).toBe("/");
    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();

    await act(async () => {
      await inFilter(() => userEvent.click(screen.getByTestId("project-selector-clear")));
    });
    await waitFor(() => expect(listed()).toEqual(ALL));
  });

  it("a new tab on the bare home starts with the remembered choice", async () => {
    remembered.filter = ["b", "e", "gone"];
    const router = await mountAt("/");
    await waitFor(() => expect(router.state.location.search).toMatchObject({ projects: "b,e" }));
    await waitFor(() => expect(listed()).toEqual(["Chat in B", "Chat in E"]));
  });

  it("a link to the home with the projects of the URL is not replaced by the persisted single filter", async () => {
    useWorkspaceStateStore.setState({ projectFilterId: "c" });
    const router = await mountAt("/?projects=a,b");
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in B"]));
    expect(router.state.location.pathname).toBe("/");
    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
  });
});
