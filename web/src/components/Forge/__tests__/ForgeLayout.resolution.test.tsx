// Copyright (c) 2025 Reliant Labs

/**
 * The two regressions that made the forge surface unusable, pinned.
 *
 * 1. REFRESH DEAD-END. `currentProject` is only set by projectStore.selectProject,
 *    which on a page load is driven exclusively from inside ModernApp — and
 *    ModernApp never mounts on a /forge/* URL. So a hard refresh left
 *    currentProject null forever and the screen rendered a sentence telling the
 *    user to select a project, on a page with nothing that could select one.
 *    The first two tests below assert the layout resolves instead: from the
 *    persisted lastProjectId (via restoreLastProject, exactly as
 *    useWorkspaceRestore does) and from a `?project=` in the URL.
 *
 * 2. NO EXIT. The forge routes sit under `_authenticated`, which renders no
 *    chrome, so there was no header, no back button and no Escape handler. The
 *    last tests assert both affordances target "/".
 *
 * The project store is mocked as a real, mutating store rather than a frozen
 * snapshot: the resolution ladder is async and reads back through getState()
 * after awaiting, so a mock that could not change would pass while the real
 * thing deadlocked.
 */

import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { z } from "zod";

interface TestProject {
  id: string;
  name: string;
  path: string;
}

const PROJECTS: TestProject[] = [
  { id: "project-a", name: "Alpha", path: "/src/alpha" },
  { id: "project-b", name: "Beta", path: "/src/beta" },
];

/** Mutable state backing the mocked store, reset per test. */
const state: {
  projects: TestProject[];
  currentProject: TestProject | null;
  lastProjectId: string | null;
} = { projects: [], currentProject: null, lastProjectId: null };

const loadProjects = vi.fn(async () => {
  state.projects = PROJECTS;
});

const selectProject = vi.fn(async (project: TestProject) => {
  state.currentProject = project;
});

/** Mirrors the real restoreLastProject: reads the persisted id out of the list. */
const restoreLastProject = vi.fn(async () => {
  if (!state.lastProjectId) return false;
  const match = state.projects.find((p) => p.id === state.lastProjectId);
  if (!match) return false;
  state.currentProject = match;
  return true;
});

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({
    projects: state.projects,
    currentProject: state.currentProject,
    loadProjects,
    selectProject,
    restoreLastProject,
  });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) =>
      selector ? selector(snapshot()) : snapshot(),
    { getState: snapshot, setState: vi.fn(), subscribe: vi.fn(() => () => undefined) }
  );
  return { useProjectStore };
});

// The header's Electron chrome is irrelevant here and the real hook subscribes
// to window/Electron APIs that do not exist in jsdom.
vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({
    isElectron: false,
    isMac: false,
    isFullscreen: false,
    trafficLightPadding: "8px",
    dragRegionStyle: {},
    noDragRegionStyle: {},
  }),
}));

vi.mock("../../../hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({
    isElectron: false,
    isMac: false,
    isFullscreen: false,
    trafficLightPadding: "8px",
    dragRegionStyle: {},
    noDragRegionStyle: {},
  }),
}));

import { ForgeLayout } from "../ForgeLayout";

/**
 * Reproduces the REAL nesting: `_authenticated` → `_forge` → the tab route.
 * A flat tree would not exercise the layout's outlet relationship, and route ids
 * shift under layout parents — which is the class of bug that broke mobile.
 */
function renderAt(initialEntry: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> });

  const authenticatedLayoutRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: "_authenticated",
    component: () => <Outlet />,
  });

  const forgeLayoutRoute = createRoute({
    getParentRoute: () => authenticatedLayoutRoute,
    id: "_forge",
    component: ForgeLayout,
  });

  const topologyRoute = createRoute({
    getParentRoute: () => forgeLayoutRoute,
    path: "/forge/topology",
    validateSearch: z.object({ project: z.string().optional() }),
    component: function TopologyProbe() {
      return <div data-testid="topology-screen">topology</div>;
    },
  });

  const statusRoute = createRoute({
    getParentRoute: () => forgeLayoutRoute,
    path: "/forge/status",
    validateSearch: z.object({
      project: z.string().optional(),
      env: z.string().optional(),
    }),
    component: () => <div data-testid="status-screen">status</div>,
  });

  const secretsRoute = createRoute({
    getParentRoute: () => forgeLayoutRoute,
    path: "/forge/secrets",
    validateSearch: z.object({
      project: z.string().optional(),
      env: z.string().optional(),
    }),
    component: () => <div data-testid="secrets-screen">secrets</div>,
  });

  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    component: () => <div data-testid="home-screen">home</div>,
  });

  const router = createRouter({
    routeTree: rootRoute.addChildren([
      authenticatedLayoutRoute.addChildren([
        forgeLayoutRoute.addChildren([topologyRoute, statusRoute, secretsRoute]),
      ]),
      homeRoute,
    ]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  });

  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  state.projects = [];
  state.currentProject = null;
  state.lastProjectId = null;
  vi.clearAllMocks();
});

describe("project resolution on a cold forge URL", () => {
  it("resolves from the persisted lastProjectId instead of dead-ending", async () => {
    // Exactly the refresh case: no currentProject (ModernApp never ran), no
    // ?project= in the URL, but a lastProjectId that IS still in localStorage.
    state.lastProjectId = "project-b";

    const router = renderAt("/forge/topology");

    await waitFor(() => {
      expect(screen.getByTestId("topology-screen")).toBeTruthy();
    });

    // It went through the same call useWorkspaceRestore makes...
    expect(restoreLastProject).toHaveBeenCalled();
    // ...and the screen rendered rather than the picker.
    expect(screen.queryByTestId("forge-project-picker")).toBeNull();

    // The resolved project is reflected into the URL, so the NEXT refresh takes
    // the cheaper `?project=` path rather than depending on localStorage again.
    await waitFor(() => {
      expect(router.state.location.search).toMatchObject({ project: "project-b" });
    });
  });

  it("resolves from a ?project= in the URL (a pasted or bookmarked link)", async () => {
    renderAt("/forge/topology?project=project-a");

    await waitFor(() => {
      expect(selectProject).toHaveBeenCalledWith(
        expect.objectContaining({ id: "project-a" })
      );
    });

    expect(loadProjects).toHaveBeenCalled();
    expect(screen.getByTestId("topology-screen")).toBeTruthy();
    expect(screen.queryByTestId("forge-project-picker")).toBeNull();
  });

  it("offers a picker — never a dead-end sentence — when nothing resolves", async () => {
    // No currentProject, no param, no persisted id. This is the state that used
    // to render "Select a project to see its forge release topology." with no
    // way to select one.
    const user = userEvent.setup();
    renderAt("/forge/topology");

    const picker = await screen.findByTestId("forge-project-picker");
    expect(picker).toBeTruthy();

    // And the picker actually selects, which is the whole point of it.
    await user.click(await screen.findByTestId("forge-project-option-project-a"));
    expect(selectProject).toHaveBeenCalledWith(
      expect.objectContaining({ id: "project-a" })
    );
  });
});

describe("leaving the forge surface", () => {
  it("closes to / via the header button", async () => {
    const user = userEvent.setup();
    state.lastProjectId = "project-a";
    const router = renderAt("/forge/topology");

    await user.click(await screen.findByTestId("forge-close"));

    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/");
    });
  });

  it("closes to / on Escape", async () => {
    const user = userEvent.setup();
    state.lastProjectId = "project-a";
    const router = renderAt("/forge/topology");

    await screen.findByTestId("topology-screen");
    await user.keyboard("{Escape}");

    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/");
    });
  });
});

describe("cross-screen navigation", () => {
  // The sidebar nav replaced the tab strip, so this reaches for the item by its
  // accessible name rather than a testid on a strip that no longer exists. The
  // name is also the stronger assertion: it pins what the user actually clicks.
  it("links the screens to each other and carries the project across", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge/topology?project=project-a");

    await screen.findByTestId("topology-screen");

    await user.click(screen.getByRole("link", { name: "Status" }));

    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/forge/status");
    });
    expect(router.state.location.search).toMatchObject({ project: "project-a" });
  });
});
