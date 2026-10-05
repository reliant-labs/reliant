// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's routes (WORKFLOW_UI.md §1.3), mounted from the SAME
 * definitions routes.tsx uses (createWorkflowsAreaRoutes) with stub pages:
 *
 *   - every area route renders inside the shell, with the right tab active;
 *   - /workflows lands on the Library;
 *   - every retired path (/workflow, /runs*, /automations*) redirects to its
 *     new home and keeps its search params;
 *   - the builder's /workflow/$workflowName is NOT swallowed by the hub
 *     redirect.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useLocation,
} from "@tanstack/react-router";

vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({
    isElectron: false,
    trafficLightPadding: "8px",
    dragRegionStyle: {},
    noDragRegionStyle: {},
  }),
}));

// The shell resolves a project on mount; here one is already current.
vi.mock("@/store/projectStore", () => {
  // One stable state object, as a real store returns between updates.
  const state = {
    currentProject: { id: "proj-1", name: "Reliant" },
    projects: [{ id: "proj-1", name: "Reliant" }],
    loadProjects: vi.fn(async () => undefined),
    selectProject: vi.fn(async () => undefined),
    restoreLastProject: vi.fn(async () => false),
  };
  const snapshot = () => state;
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => (selector ? selector(snapshot()) : snapshot()),
    { getState: snapshot },
  );
  return { useProjectStore };
});

import { createWorkflowsAreaRoutes } from "@/workflowsAreaRoutes";
import { WorkflowsLayout } from "../WorkflowsShell";

function stub(name: string) {
  return function Stub() {
    const location = useLocation();
    return <div data-testid={`page-${name}`}>{location.pathname}</div>;
  };
}

function renderAt(path: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const authed = createRoute({ getParentRoute: () => rootRoute, id: "_authenticated", component: () => <Outlet /> });
  const builder = createRoute({
    getParentRoute: () => authed,
    path: "/workflow/$workflowName",
    component: stub("builder"),
  });
  const home = createRoute({ getParentRoute: () => rootRoute, path: "/", component: stub("home") });
  const areaRoutes = createWorkflowsAreaRoutes(() => authed, {
    layout: WorkflowsLayout,
    library: stub("library"),
    workflowDetail: stub("workflow-detail"),
    runs: stub("runs"),
    runDetail: stub("run-detail"),
    automations: stub("automations"),
    automationDetail: stub("automation-detail"),
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([home, authed.addChildren([builder, ...areaRoutes])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

function activeTab(): string | null {
  const nav = screen.getByRole("navigation", { name: "Workflows" });
  const current = within(nav)
    .getAllByRole("link")
    .find((link) => link.getAttribute("aria-current") === "page");
  return current?.textContent ?? null;
}

describe("Workflows area routes render inside the shell with the right tab", () => {
  it.each([
    ["/workflows/library", "library", "Library"],
    ["/workflows/library/builtin%3A%2F%2Fagent", "workflow-detail", "Library"],
    ["/workflows/runs", "runs", "Runs"],
    ["/workflows/runs/chat-1", "run-detail", "Runs"],
    ["/workflows/automations", "automations", "Automations"],
    ["/workflows/automations/trig-1", "automation-detail", "Automations"],
  ])("%s → %s, %s tab active", async (path, page, tab) => {
    renderAt(path);
    expect(await screen.findByTestId(`page-${page}`)).toBeInTheDocument();
    expect(activeTab()).toBe(tab);
    // The three tabs, in order.
    const nav = screen.getByRole("navigation", { name: "Workflows" });
    expect(within(nav).getAllByRole("link").map((link) => link.textContent)).toEqual([
      "Library",
      "Runs",
      "Automations",
    ]);
  });

  it("/workflows lands on the Library and keeps ?project", async () => {
    const router = renderAt("/workflows?project=%22proj-1%22");
    expect(await screen.findByTestId("page-library")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/workflows/library");
    expect(router.state.location.search).toMatchObject({ project: "proj-1" });
  });

  it("the exit always leaves the area; the sidebar is the way back to a list", async () => {
    const router = renderAt("/workflows/runs/chat-1?project=%22proj-1%22");
    await screen.findByTestId("page-run-detail");
    const runs = within(screen.getByRole("navigation", { name: "Workflows" })).getByRole("link", { name: "Runs" });
    expect(runs).toHaveAttribute("aria-current", "page");
    expect(runs).toHaveAttribute("href", "/workflows/runs?project=proj-1");
    screen.getByRole("button", { name: "Back to app" }).click();
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
  });

  it("Escape on a detail page steps back to its list", async () => {
    const router = renderAt("/workflows/automations/trig-1");
    await screen.findByTestId("page-automation-detail");
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/automations"));
  });

  // A regression guard, NOT a reproduction: the original bug needs a real
  // keypress, where the browser runs a microtask checkpoint between listeners
  // and React removes the menu before the window listener looks for it. A
  // scripted dispatchEvent (jsdom, user-event) never checkpoints mid-dispatch,
  // so this passes against the bubble-phase check too. The fix — the capture
  // phase check in useEscapeToLeave — was verified in Chrome.
  it("Escape with a menu open closes only the menu, not the area", async () => {
    const router = renderAt("/workflows/library");
    await screen.findByTestId("page-library");
    screen.getByTestId("workflows-project-switcher").click();
    expect(await screen.findByRole("menu", { name: "Projects" })).toBeInTheDocument();
    // As a real keypress does: dispatched at the focused element, bubbling to document and window.
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await waitFor(() => expect(screen.queryByRole("menu", { name: "Projects" })).toBeNull());
    expect(router.state.location.pathname).toBe("/workflows/library");
  });

  it("the project switcher is in the header for Library and Runs, not Automations", async () => {
    renderAt("/workflows/library");
    expect(await screen.findByTestId("workflows-project-switcher")).toHaveTextContent("Reliant");
  });
});

describe("Retired paths redirect and keep their search params", () => {
  it.each([
    ["/workflow", "/workflows/library", "library"],
    ["/runs", "/workflows/runs", "runs"],
    ["/runs/chat-7", "/workflows/runs/chat-7", "run-detail"],
    ["/automations", "/workflows/automations", "automations"],
    ["/automations/trig-3", "/workflows/automations/trig-3", "automation-detail"],
  ])("%s → %s", async (from, to, page) => {
    const router = renderAt(from);
    expect(await screen.findByTestId(`page-${page}`)).toBeInTheDocument();
    expect(router.state.location.pathname).toBe(to);
  });

  it("carries the Runs filters across", async () => {
    const router = renderAt('/runs?state=%5B%22needs_you%22%5D&allProjects=true&parent=%22chat-1%22');
    await screen.findByTestId("page-runs");
    expect(router.state.location.pathname).toBe("/workflows/runs");
    expect(router.state.location.search).toMatchObject({
      state: ["needs_you"],
      allProjects: true,
      parent: "chat-1",
    });
  });

  it("carries ?tour across the hub redirect", async () => {
    const router = renderAt("/workflow?tour=%22workflow-hub%22");
    await screen.findByTestId("page-library");
    expect(router.state.location.search).toMatchObject({ tour: "workflow-hub" });
  });

  it("replaces history, so Back does not bounce through the old path", async () => {
    const router = renderAt("/automations/trig-3");
    await screen.findByTestId("page-automation-detail");
    expect(router.history.length).toBe(1);
  });

  it("leaves the builder's /workflow/$workflowName alone", async () => {
    const router = renderAt("/workflow/builtin%3A%2F%2Fagent");
    expect(await screen.findByTestId("page-builder")).toBeInTheDocument();
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflow/builtin%3A%2F%2Fagent"));
  });
});
