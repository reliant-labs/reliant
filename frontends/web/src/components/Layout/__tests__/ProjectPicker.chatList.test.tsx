/**
 * The shell's project scope, choose N of M (hooks/useChatProjectFilter),
 * scopes the chat list from any route. Its one control is the nav's project
 * switcher (sidebar/ProjectSwitcher.tsx); this drives the hook's set/clear as
 * the switcher does, so the assertions are on the URL and the list.
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
 * The real router, stores and query hooks run here; only the
 * transport (and the settings read) is fake, so the assertions are on the
 * ListChats requests the server would see and the chats the list renders.
 * The list is mounted once, outside the routes, as the shell mounts the
 * sidebar.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
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
import { useChatListWithWorkspaces } from "../../../hooks/chat-queries";
import type { Project } from "../../../hooks/project-queries";
import { resetChatProjectFilterForTests, useChatProjectFilter, type ChatProjectFilter } from "../../../hooks/useChatProjectFilter";
import { setTransport } from "../../../lib/connect";
import { queryClient } from "../../../lib/query-client";
import { mockTransport } from "../../../lib/test-utils";
import { indexSearchSchema } from "../../../routeSchemas";
import { scopeDefault } from "../../../lib/urlState/shellScope";
import { resetShellScopeForTests } from "../../../lib/urlState/shellScopeStore";
import { useActiveOrgStore } from "../../../store/activeOrgStore";
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

/** The filter the shell rendered last: what the switcher's picks call. */
let current: ChatProjectFilter | null = null;

/** The sidebar's wiring, minus its layout: the filter's state and the list it scopes. */
function ChatListShell() {
  const filterId = useWorkspaceStateStore((state) => state.projectFilterId);
  const location = useLocation();
  const filter = useChatProjectFilter({ sync: { pathname: location.pathname, search: location.search } });
  current = filter;
  const { data } = useChatListWithWorkspaces(filter.mode === "many" ? filter.ids : filterId);
  return (
    <>
      <span data-testid="filter-state">{`${filter.mode}:${filter.ids.join(",")}`}</span>
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

/** Choose these projects ([] for every project), as the switcher does. */
async function choose(ids: string[]) {
  await act(async () => {
    current!.set(ids);
  });
}
const filterState = () => screen.getByTestId("filter-state").textContent;

const listed = () => within(screen.getByTestId("chat-list")).queryAllByRole("listitem").map((li) => li.textContent);
const ALL = ["Chat in A", "Chat in B", "Chat in C", "Chat in D", "Chat in E"];

/** A fresh page: no cache, no in-memory filter, the persisted single filter kept. */
function newPage() {
  cleanup();
  queryClient.clear();
  resetChatProjectFilterForTests();
  current = null;
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

describe("the shell's project scope, choose N of M, scoping the chat list", () => {
  it("lists only the chats of the two projects of five chosen, in one ListChats naming both", async () => {
    const router = await mountAt("/");
    await waitFor(() => expect(listed()).toEqual(ALL));

    await choose(["b", "d"]);

    await waitFor(() => expect(listed()).toEqual(["Chat in B", "Chat in D"]));
    expect(requests.at(-1)?.projectIds.sort()).toEqual(["b", "d"]);
    expect(router.state.location.pathname).toBe("/");
    expect(router.state.location.search).toMatchObject({ projects: "b,d" });
    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
    expect(filterState()).toBe("many:b,d");
    // Remembered for the next tab.
    expect(remembered.writes.at(-1)).toEqual(["b", "d"]);
  });

  it("keeps the choice through a refresh: the URL is the choice", async () => {
    await mountAt("/?projects=a,e");
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in E"]));

    newPage();
    await mountAt("/?projects=a,e");
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in E"]));
    expect(filterState()).toBe("many:a,e");
  });

  it("clearing it lists every project's chats again, and forgets the choice", async () => {
    const router = await mountAt("/?projects=a,c");
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in C"]));

    await act(async () => {
      current!.clear();
    });

    await waitFor(() => expect(listed()).toEqual(ALL));
    expect(router.state.location.search).not.toHaveProperty("projects");
    expect(filterState()).toBe("all:");
    expect(remembered.writes.at(-1)).toEqual([]);
  });

  it("one project is the single-project filter: /project/<id>, and the old setting still loads", async () => {
    // A persisted single filter (what old builds write) at the bare home.
    useWorkspaceStateStore.setState({ projectFilterId: "c" });
    const router = await mountAt("/");
    await waitFor(() => expect(router.state.location.pathname).toBe("/project/c"));
    await waitFor(() => expect(listed()).toEqual(["Chat in C"]));
    expect(requests.at(-1)).toEqual({ projectId: "c", projectIds: [] });

    // Another single project, by the old route.
    await choose(["b"]);
    await waitFor(() => expect(listed()).toEqual(["Chat in B"]));
    expect(router.state.location.pathname).toBe("/project/b");
    expect(useWorkspaceStateStore.getState().projectFilterId).toBe("b");
    expect(useProjectStore.getState().currentProject?.id).toBe("b");
    expect(touchProject.mock.calls.map(([id]) => id)).not.toContain("c");
  });

  it("goes from one project to several and back to all", async () => {
    const router = await mountAt("/project/a");
    await waitFor(() => expect(listed()).toEqual(["Chat in A"]));

    await choose(["a", "e"]);
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in E"]));
    expect(router.state.location.pathname).toBe("/");
    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();

    await choose([]);
    await waitFor(() => expect(listed()).toEqual(ALL));
  });

  it("a new tab on the bare home starts with the remembered choice", async () => {
    remembered.filter = ["b", "e", "gone"];
    const router = await mountAt("/");
    await waitFor(() => expect(router.state.location.search).toMatchObject({ projects: "b,e" }));
    await waitFor(() => expect(listed()).toEqual(["Chat in B", "Chat in E"]));
  });

  it("a reload of the bare home takes this tab's own scope over the remembered choice", async () => {
    useActiveOrgStore.setState({ userId: "u-tabs" });
    resetShellScopeForTests();
    scopeDefault.set({ mode: "many", projectIds: ["a", "c"] });
    remembered.filter = ["b", "e"];
    try {
      const router = await mountAt("/");
      await waitFor(() => expect(router.state.location.search).toMatchObject({ projects: "a,c" }));
      await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in C"]));
    } finally {
      scopeDefault.set(undefined);
      useActiveOrgStore.setState({ userId: null });
    }
  });

  it("a bare home filled with All says so in its URL", async () => {
    useActiveOrgStore.setState({ userId: "u-tabs" });
    resetShellScopeForTests();
    scopeDefault.set({ mode: "all" });
    remembered.filter = ["b", "e"];
    try {
      const router = await mountAt("/");
      await waitFor(() => expect(router.state.location.search).toMatchObject({ projects: "all" }));
      await waitFor(() => expect(listed()).toEqual(ALL));
    } finally {
      scopeDefault.set(undefined);
      useActiveOrgStore.setState({ userId: null });
    }
  });

  it("a link to the home with the projects of the URL is not replaced by the persisted single filter", async () => {
    useWorkspaceStateStore.setState({ projectFilterId: "c" });
    const router = await mountAt("/?projects=a,b");
    await waitFor(() => expect(listed()).toEqual(["Chat in A", "Chat in B"]));
    expect(router.state.location.pathname).toBe("/");
    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
  });
});
