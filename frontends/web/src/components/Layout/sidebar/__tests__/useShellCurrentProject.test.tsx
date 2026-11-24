// Copyright (c) 2026 Reliant Labs

/**
 * A PAGE OUTSIDE THE CHAT SCREEN HAS THE NAV'S PROJECT ON A COLD LOAD (reliant#3445).
 *
 * ModernApp sets the context project on a page load, and it mounts only on
 * the chat routes; since #3419 the nav no longer selects one either. So
 * `/inbox` and `/settings/billing`, opened cold, had no current project while
 * the nav showed one. useShellCurrentProject reads the nav's own resolution
 * (useShellProject): the page and the nav agree, and nothing is selected.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";

import type { ProjectCatalog, ProjectCatalogRow, AppOnlyCatalogRow } from "@/services/projects/catalog";
import type { Project } from "@/store/projectStore";

/** The page's route, for the nav's side of the comparison (the hook itself reads no route). */
const location = { pathname: "/inbox", search: {} as Record<string, unknown> };

const project = (id: string): Project =>
  ({ id, name: id, path: `/${id}`, is_git_repo: true, last_active: "", created_at: "", updated_at: "", is_forge: false }) as Project;
const row = (id: string, activityAt: number): ProjectCatalogRow => ({
  kind: "project",
  key: id,
  name: id,
  activityAt,
  project: project(id),
  shared: false,
  repos: [],
  apps: [],
  checkouts: [],
  hasCheckout: true,
});
const appOnly: AppOnlyCatalogRow = {
  kind: "app",
  key: "app:storefront",
  name: "storefront",
  activityAt: 0,
  app: { name: "storefront", state: "live" },
} as AppOnlyCatalogRow;
// p1 is the most recently active.
const catalog: ProjectCatalog = {
  orgId: "org",
  hostedAppsState: "ok",
  rows: [row("p1", 4), row("p2", 3), row("p3", 2), row("p4", 1), appOnly],
};

vi.mock("../../../Projects/ProjectSelector", () => ({ useSelectorCatalog: () => ({ catalog, loading: false }) }));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects: catalog.rows.flatMap((r) => (r.kind === "project" ? [r.project] : [])), isLoading: false }),
}));
vi.mock("@/hooks/chat-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/chat-queries")>()),
  useChat: () => ({ data: undefined }),
}));
vi.mock("@/hooks/sidebar-project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/sidebar-project-queries")>()),
  useSidebarProjectPrefs: () => ({ prefs: { filter: [] } }),
}));

const { useShellCurrentProject } = await import("../useShellCurrentProject");
const { SEVERAL_PROJECTS_PICK, useShellLastPick, useShellProject } = await import("../shellProject");
const { useChatProjectFilterStore } = await import("@/hooks/useChatProjectFilter");
const { useProjectStore } = await import("@/store/projectStore");
const { publishShellScope, resetShellScopeForTests } = await import("@/lib/urlState/shellScopeStore");

/** The page's project and the nav's, on the same route. */
function renderBoth() {
  return renderHook(() => ({ page: useShellCurrentProject(), nav: useShellProject(location.pathname, location.search) }));
}

beforeEach(() => {
  location.pathname = "/inbox";
  location.search = {};
  useChatProjectFilterStore.setState({ many: [] });
  useProjectStore.setState({ currentProject: null });
});
afterEach(() => {
  resetShellScopeForTests();
  useShellLastPick.setState({ key: null, several: [], focus: null });
  useChatProjectFilterStore.setState({ many: [] });
  useProjectStore.setState({ currentProject: null });
});

describe("useShellCurrentProject", () => {
  it.each(["/inbox", "/settings/billing", "/m/inbox"])("on a cold load of %s it is the nav's last pick, with no context project", (path) => {
    location.pathname = path;
    useShellLastPick.setState({ key: "p2", focus: "p2" });

    const { result } = renderBoth();

    expect(result.current.page?.id).toBe("p2");
    expect(result.current.nav.projectId).toBe("p2");
  });

  it("with nothing remembered it is the project the nav falls back on (the most recent)", () => {
    location.pathname = "/settings/billing";

    const { result } = renderBoth();

    expect(result.current.page?.id).toBe("p1");
    expect(result.current.nav.projectId).toBe("p1");
  });

  it("follows the URL's choice of several: the focused project among them", () => {
    useChatProjectFilterStore.setState({ many: ["p3", "p4"] });
    useShellLastPick.setState({ key: SEVERAL_PROJECTS_PICK, several: ["p3", "p4"], focus: "p4" });

    const { result } = renderBoth();

    expect(result.current.nav.mode).toBe("many");
    expect(result.current.page?.id).toBe("p4");
    expect(result.current.nav.projectId).toBe("p4");
  });

  it("is the nav's pick over a stale context project, without selecting it", () => {
    useProjectStore.setState({ currentProject: project("p3") });
    useShellLastPick.setState({ key: "p2", focus: "p2" });

    const { result } = renderBoth();

    expect(result.current.page?.id).toBe("p2");
    // It reads; it never makes the project current (#3419).
    expect(useProjectStore.getState().currentProject?.id).toBe("p3");
  });

  it("never selects a project on a cold load", () => {
    useShellLastPick.setState({ key: "p2", focus: "p2" });

    renderBoth();

    expect(useProjectStore.getState().currentProject).toBeNull();
  });

  it("is no project when the nav shows an app with no code here", () => {
    useShellLastPick.setState({ key: "app:storefront", focus: "app:storefront" });

    const { result } = renderBoth();

    expect(result.current.nav.key).toBe("app:storefront");
    expect(result.current.page).toBeNull();
  });

  it("is the one project the nav on screen shows, over its own resolution", () => {
    useShellLastPick.setState({ key: "p2", focus: "p2" });
    const { result, rerender } = renderBoth();
    expect(result.current.page?.id).toBe("p2");

    // The sidebar published another project (its route's, say): the page takes the nav's.
    publishShellScope({ scope: { mode: "one", projectId: "p4" }, focus: null });
    rerender();
    expect(result.current.page?.id).toBe("p4");
  });
});
