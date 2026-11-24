/**
 * The phone's URL → store bridge (MobileScopeSync): the URL is the only view
 * state, and the stores follow it.
 *
 *   - a bare chat list is filled from the tab's last scope, else every
 *     project, with a replace (no step Back retraces);
 *   - a bare `/m/new` names the project it opens on, so Back after a pick
 *     returns to it;
 *   - the context project follows `/m/new?project=`, else the scope's one
 *     project, but never on a chat's own page;
 *   - the URL's scope is published, and the chat store's list reloads on a
 *     change of it.
 */
import { cleanup, render, waitFor } from "@testing-library/react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  retainSearchParams,
  type AnyRouter,
} from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const PROJECTS = [
  { id: "p1", name: "shopfront", path: "/p1" },
  { id: "p2", name: "docs-site", path: "/p2" },
  { id: "p3", name: "landing", path: "/p3" },
];
vi.mock("@/hooks/project-queries", async (original) => ({
  ...(await original<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects: PROJECTS, isLoading: false, error: null, refetch: () => {} }),
}));

import { mobileNewChatSearchSchema, PROJECTS_CARRIED } from "@/routeSchemas";
import { scopeDefault } from "@/lib/urlState/shellScope";
import { resetShellScopeForTests, shellScopeNow } from "@/lib/urlState/shellScopeStore";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useChatStore } from "@/store/chatStore";
import { useProjectStore } from "@/store/projectStore";

import { MobileScopeSync } from "../MobileScopeSync";

let router: AnyRouter;
const selectProject = vi.fn();
const loadChats = vi.fn();

async function phoneAt(url: string) {
  // The root carries the shell scope, as routes.tsx declares it (its search has no schema).
  const root = createRootRoute({ search: { middlewares: [retainSearchParams([...PROJECTS_CARRIED]) as never] } });
  const shell = createRoute({
    getParentRoute: () => root,
    id: "_mobile",
    component: () => (
      <>
        <MobileScopeSync />
        <Outlet />
      </>
    ),
  });
  const chats = createRoute({ getParentRoute: () => shell, path: "/m/chats" });
  const chat = createRoute({ getParentRoute: () => shell, path: "/m/chats/$chatId" });
  const draft = createRoute({ getParentRoute: () => shell, path: "/m/new", validateSearch: mobileNewChatSearchSchema });
  const projects = createRoute({ getParentRoute: () => shell, path: "/m/projects" });
  router = createRouter({
    routeTree: root.addChildren([shell.addChildren([chats, chat, draft, projects])]),
    history: createMemoryHistory({ initialEntries: [url] }),
  }) as unknown as AnyRouter;
  await router.load();
  render(<RouterProvider router={router} />);
}

const at = () => `${router.history.location.pathname}${router.history.location.search}`;
const entries = () => router.history.length;

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  resetShellScopeForTests();
  useActiveOrgStore.setState({ userId: "u1", effectiveId: "o1" } as never);
  useProjectStore.setState({ currentProject: PROJECTS[0] as never });
  selectProject.mockReset();
  selectProject.mockImplementation(async (project: (typeof PROJECTS)[number]) => {
    useProjectStore.setState({ currentProject: project as never });
  });
  useProjectStore.setState({ selectProject } as never);
  loadChats.mockReset();
  loadChats.mockResolvedValue(undefined);
  useChatStore.setState({ loadChats } as never);
});

afterEach(() => {
  cleanup();
});

describe("MobileScopeSync", () => {
  it("fills a bare chat list from the tab's last scope, in place", async () => {
    scopeDefault.set({ mode: "one", projectId: "p2" });
    await phoneAt("/m/chats");
    await waitFor(() => expect(at()).toBe("/m/chats?projects=p2"));
    expect(entries()).toBe(1);
    await waitFor(() => expect(selectProject).toHaveBeenCalledWith(expect.objectContaining({ id: "p2" }), { restoreWorkspace: false }));
    expect(shellScopeNow()).toEqual({ mode: "one", projectId: "p2" });
  });

  it("fills a bare chat list with every project when the tab has no scope, or one since removed", async () => {
    scopeDefault.set({ mode: "one", projectId: "p-gone" });
    await phoneAt("/m/chats");
    await waitFor(() => expect(at()).toBe("/m/chats?projects=all"));
    expect(selectProject).not.toHaveBeenCalled();
  });

  it("moves the context to the URL scope's project, and reloads the list on a change", async () => {
    await phoneAt("/m/chats?projects=p2");
    await waitFor(() => expect(useProjectStore.getState().currentProject?.id).toBe("p2"));
    expect(loadChats).not.toHaveBeenCalled();
    await router.navigate({ to: "/m/chats", search: { projects: "p3" } as never });
    await waitFor(() => expect(useProjectStore.getState().currentProject?.id).toBe("p3"));
    expect(loadChats).toHaveBeenCalledTimes(1);
    // The pick is this tab's default for an empty URL.
    expect(scopeDefault.get()).toEqual({ mode: "one", projectId: "p3" });
  });

  it("leaves a chat's own page to its chat", async () => {
    await phoneAt("/m/chats/chat-1?projects=p2");
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(selectProject).not.toHaveBeenCalled();
    expect(at()).toBe("/m/chats/chat-1?projects=p2");
  });

  it("names a bare /m/new's project in place: the scope's, else the context's", async () => {
    await phoneAt("/m/new?projects=p2");
    await waitFor(() => expect(at()).toBe("/m/new?projects=p2&project=p2"));
    expect(entries()).toBe(1);
    cleanup();
    useProjectStore.setState({ currentProject: PROJECTS[2] as never });
    await phoneAt("/m/new?projects=all");
    await waitFor(() => expect(at()).toBe("/m/new?projects=all&project=p3"));
  });

  it("moves the context to /m/new's project, over the scope, and back with Back", async () => {
    await phoneAt("/m/new?projects=p2&project=p2");
    await waitFor(() => expect(useProjectStore.getState().currentProject?.id).toBe("p2"));
    await router.navigate({ to: "/m/new", search: (prev: Record<string, unknown>) => ({ ...prev, project: "p3" }) } as never);
    await waitFor(() => expect(useProjectStore.getState().currentProject?.id).toBe("p3"));
    expect(at()).toBe("/m/new?projects=p2&project=p3");
    router.history.back();
    await waitFor(() => expect(at()).toBe("/m/new?projects=p2&project=p2"));
    await waitFor(() => expect(useProjectStore.getState().currentProject?.id).toBe("p2"));
  });
});
