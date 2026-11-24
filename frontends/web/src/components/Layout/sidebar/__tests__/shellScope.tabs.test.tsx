// Copyright (c) 2026 Reliant Labs

/**
 * THE SHELL'S SCOPE IS THE TAB'S OWN (owner, 2026-11-24: the URL is the only
 * source of view state).
 *
 *   - Storage only fills an EMPTY URL: this tab's own default first
 *     (sessionStorage), then the last scope any tab showed (localStorage, the
 *     seed of a new tab), then the person's `sidebar.projects` setting.
 *   - Two tabs keep their own scopes through a reload and a re-read of the
 *     shared setting (it refetches on focus): no tab adopts another's choice.
 *   - All and the focus are the URL's (`?projects=all`, `?focus=`), carried to
 *     the next page like the filter of several, and Back brings each back.
 *   - What the nav shows is what every reader outside React takes.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, renderHook, waitFor } from "@testing-library/react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  retainSearchParams,
  useRouterState,
} from "@tanstack/react-router";

import { createTabDefault } from "@/lib/urlState/tabDefault";
import { projectsParam, type ShellScope } from "@/lib/urlState/shellScope";
import type { ProjectCatalog, ProjectCatalogRow } from "@/services/projects/catalog";
import type { Project } from "@/store/projectStore";

const row = (id: string): ProjectCatalogRow => ({
  kind: "project",
  key: id,
  name: id,
  activityAt: 0,
  project: { id, name: id, path: `/${id}`, is_git_repo: true, last_active: "", created_at: "", updated_at: "", is_forge: false } as Project,
  shared: false,
  repos: [],
  apps: [],
  checkouts: [],
  hasCheckout: true,
});
const catalog: ProjectCatalog = { orgId: "org", hostedAppsState: "ok", rows: ["p1", "p2", "p3", "p4"].map(row) };

const shared = vi.hoisted(() => ({ filter: [] as string[] }));
vi.mock("../../../Projects/ProjectSelector", () => ({ useSelectorCatalog: () => ({ catalog, loading: false }) }));
vi.mock("@/hooks/chat-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/chat-queries")>()),
  useChat: (id: string | undefined) => ({ data: id ? { id, projectId: "p1" } : undefined }),
}));
// The shared setting, as another tab last wrote it: the nav must never read it.
vi.mock("@/hooks/sidebar-project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/sidebar-project-queries")>()),
  useSidebarProjectPrefs: () => ({ prefs: { filter: shared.filter }, writable: true, loadFailed: false, setFilter: () => {} }),
}));

const { SEVERAL_PROJECTS_PICK, usePublishShellScope, useShellLastPick, useShellProject } = await import("../shellProject");
const { useChatProjectFilterStore } = await import("@/hooks/useChatProjectFilter");
const { fillScope, resetShellScopeForTests, shellScopeNow, shellProjectNow } = await import("@/lib/urlState/shellScopeStore");
const { navigateWithFocus, navigateWithShellScope } = await import("../../shellScopeNav");
const { PROJECTS_CARRIED, projectsCarrySearchSchema } = await import("@/routeSchemas");

/** A Storage over a Map: each tab's sessionStorage, and the one localStorage they share. */
function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    get length() {
      return map.size;
    },
    clear: () => map.clear(),
    getItem: (key) => map.get(key) ?? null,
    key: (index) => [...map.keys()][index] ?? null,
    removeItem: (key) => void map.delete(key),
    setItem: (key, value) => void map.set(key, String(value)),
  };
}

const codec = { parse: (raw: string) => projectsParam.parse(raw), serialize: (scope: ShellScope) => projectsParam.serialize(scope) ?? "" };

/** A browser: tabs, each with its own sessionStorage, over one localStorage. */
function browser() {
  const localStorage = memoryStorage();
  return () => {
    const sessionStorage = memoryStorage();
    return createTabDefault<ShellScope>({ key: "u.org.scope", ...codec, storage: { tab: () => sessionStorage, seed: () => localStorage } });
  };
}

const listed = new Set(["p1", "p2", "p3", "p4"]);
const many = (...projectIds: string[]): ShellScope => ({ mode: "many", projectIds });

beforeEach(() => {
  shared.filter = [];
  resetShellScopeForTests();
  useChatProjectFilterStore.setState({ many: [] });
});
afterEach(() => {
  cleanup();
  useShellLastPick.setState({ key: null, several: [], focus: null });
  useChatProjectFilterStore.setState({ many: [] });
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe("filling an empty URL", () => {
  it("takes the tab's own default, else the remembered choice of several, else the context project", () => {
    expect(fillScope({ tabDefault: { mode: "all" }, setting: ["p1", "p2"], listed, contextProjectId: "p3" })).toEqual({ mode: "all" });
    expect(fillScope({ tabDefault: many("p2", "gone", "p3"), setting: [], listed, contextProjectId: null })).toEqual(many("p2", "p3"));
    // The setting's projects no longer listed are left out; fewer than two is no choice of several.
    expect(fillScope({ tabDefault: undefined, setting: ["p1", "gone", "p2"], listed, contextProjectId: "p3" })).toEqual(many("p1", "p2"));
    expect(fillScope({ tabDefault: undefined, setting: ["p1", "gone"], listed, contextProjectId: "p3" })).toEqual({ mode: "one", projectId: "p3" });
    expect(fillScope({ tabDefault: undefined, setting: [], listed, contextProjectId: null })).toBeNull();
  });

  it("waits for what decides it", () => {
    expect(fillScope({ tabDefault: many("p1", "p2"), setting: [], listed: undefined, contextProjectId: null })).toBe("wait");
    expect(fillScope({ tabDefault: undefined, setting: undefined, listed, contextProjectId: null })).toBe("wait");
  });
});

describe("two tabs choosing different scopes", () => {
  it("each fills a reload with its own choice, whatever the shared setting says on a refetch", () => {
    const tab = browser();
    const a = tab();
    const b = tab();
    a.set(many("p1", "p2"));
    b.set(many("p3", "p4"));
    const fill = (t: typeof a, setting: string[]) => fillScope({ tabDefault: t.get(), setting, listed, contextProjectId: null });
    expect(fill(a, ["p3", "p4"])).toEqual(many("p1", "p2"));
    expect(fill(b, ["p3", "p4"])).toEqual(many("p3", "p4"));
    expect(fill(a, ["p2", "p4"])).toEqual(many("p1", "p2"));
  });

  it("a new tab starts from the last scope any tab showed, then keeps its own", () => {
    const tab = browser();
    const a = tab();
    a.set(many("p1", "p2"));
    const fresh = tab();
    expect(fresh.get()).toEqual(many("p1", "p2"));
    a.set({ mode: "all" });
    expect(fresh.get()).toEqual(many("p1", "p2"));
  });

  it("the nav keeps this tab's choice of several when another tab's reaches the shared setting", () => {
    useShellLastPick.setState({ key: SEVERAL_PROJECTS_PICK, several: ["p1", "p2"] });
    shared.filter = ["p3", "p4"];
    const { result } = renderHook(() => useShellProject("/inbox", {}));
    expect(result.current.mode).toBe("many");
    expect([...result.current.ids].sort()).toEqual(["p1", "p2"]);
    expect([...useChatProjectFilterStore.getState().many].sort()).toEqual(["p1", "p2"]);
  });
});

describe("All and the focus in the URL", () => {
  async function mountAt(path: string) {
    function Probe() {
      const location = useRouterState({ select: (s) => s.location });
      const shell = useShellProject(location.pathname, location.search as Record<string, unknown>);
      usePublishShellScope(shell, location.search as Record<string, unknown>);
      return <div data-testid="shell" data-mode={shell.mode} data-key={shell.key ?? ""} />;
    }
    const root = createRootRoute({
      component: () => (
        <>
          <Probe />
          <Outlet />
        </>
      ),
      validateSearch: projectsCarrySearchSchema,
      search: { middlewares: [retainSearchParams([...PROJECTS_CARRIED])] },
    });
    const page = () => null;
    const routes = [
      createRoute({ getParentRoute: () => root, path: "/", component: page }),
      createRoute({ getParentRoute: () => root, path: "/c/$chatId", component: page }),
      createRoute({ getParentRoute: () => root, path: "/forge/p/$app", component: page }),
    ];
    const history = createMemoryHistory({ initialEntries: [path] });
    const router = createRouter({ routeTree: root.addChildren(routes), history });
    (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = router;
    await router.load();
    const view = render(<RouterProvider router={router} />);
    const mode = () => view.getByTestId("shell").getAttribute("data-mode");
    const focusKey = () => view.getByTestId("shell").getAttribute("data-key");
    return { router, history, mode, focusKey };
  }

  it("shows every project from ?projects=all, carries it to the next page, and Back undoes the pick", async () => {
    const { router, history, mode } = await mountAt("/c/chat-1");
    await waitFor(() => expect(mode()).toBe("one"));
    expect(shellProjectNow()).toBe("p1");

    act(() => navigateWithShellScope({ mode: "all" }));
    await waitFor(() => expect(mode()).toBe("all"));
    expect(router.state.location.search).toEqual({ projects: "all" });
    // Every reader outside React takes what the nav shows.
    await waitFor(() => expect(shellScopeNow()).toEqual({ mode: "all" }));

    await act(() => router.navigate({ to: "/c/$chatId", params: { chatId: "chat-2" } }));
    expect(router.state.location.search).toEqual({ projects: "all" });
    expect(mode()).toBe("all");

    act(() => history.back());
    act(() => history.back());
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    // The tab's memory of All stays for pages that do not carry it; the URL here names one project's chat.
    expect(router.state.location.pathname).toBe("/c/chat-1");
  });

  it("opens the focused project's sections from ?focus=, beside the scope it keeps", async () => {
    const { router, focusKey, mode } = await mountAt("/forge/p/app?projects=p2,p3");
    act(() => useChatProjectFilterStore.getState().setMany(["p2", "p3"]));
    await waitFor(() => expect(mode()).toBe("many"));

    act(() => navigateWithFocus("/", "p3"));
    await waitFor(() => expect(router.state.location.search).toEqual({ projects: "p2,p3", focus: "p3" }));
    await waitFor(() => expect(focusKey()).toBe("p3"));
    expect(useShellLastPick.getState().focus).toBe("p3");
  });
});
