// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's routes (WORKFLOW_UI.md §1.3), mounted from the SAME
 * definitions routes.tsx uses (createWorkflowsAreaRoutes) with stub pages:
 *
 *   - every area route renders inside the shell, with the right tab active,
 *     and the shell has two tabs (Workflows, Runs) and no badge;
 *   - /workflows lands on the Workflows list;
 *   - every retired path (/workflow, /runs*, /automations*,
 *     /workflows/automations) redirects to its new home and keeps its search
 *     params; an automation id resolves through the automation redirect;
 *   - the builder's /workflow/$workflowName is NOT swallowed by the hub
 *     redirect.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

const chrome = vi.hoisted(() => ({ isElectron: false, trafficLightPadding: "0px" }));
vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({
    ...chrome,
    dragRegionStyle: chrome.isElectron ? { WebkitAppRegion: "drag" } : {},
    noDragRegionStyle: chrome.isElectron ? { WebkitAppRegion: "no-drag" } : {},
  }),
}));

beforeEach(() => {
  chrome.isElectron = false;
  chrome.trafficLightPadding = "0px";
});
afterEach(() => vi.unstubAllGlobals());

// Keep these route/chrome tests independent of RPCs and generated query hooks.
vi.mock("@/hooks/project-queries", () => ({
  useProjects: () => ({ projects: [{ id: "proj-1", name: "Reliant" }] }),
  fetchProjects: vi.fn(async () => [{ id: "proj-1", name: "Reliant" }]),
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
    automationRedirect: stub("automation-redirect"),
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
    ["/workflows/library", "library", "Workflows"],
    ["/workflows/library/builtin%3A%2F%2Fagent", "workflow-detail", "Workflows"],
    ["/workflows/runs", "runs", "Runs"],
    ["/workflows/runs/chat-1", "run-detail", "Runs"],
  ])("%s → %s, %s tab active", async (path, page, tab) => {
    renderAt(path);
    expect(await screen.findByTestId(`page-${page}`)).toBeInTheDocument();
    expect(activeTab()).toBe(tab);
    // Two tabs: the workflows (with what runs by itself, inline) and the log.
    const nav = screen.getByRole("navigation", { name: "Workflows" });
    expect(within(nav).getAllByRole("link").map((link) => link.textContent)).toEqual(["Workflows", "Runs"]);
  });

  it("an automation id renders the resolving redirect inside the shell", async () => {
    renderAt("/workflows/automations/trig-1");
    expect(await screen.findByTestId("page-automation-redirect")).toBeInTheDocument();
  });

  it("no tab carries a badge: the Inbox is the one count of what needs you", async () => {
    renderAt("/workflows/library");
    const nav = await screen.findByRole("navigation", { name: "Workflows" });
    for (const link of within(nav).getAllByRole("link")) expect(link.textContent).toMatch(/^(Workflows|Runs)$/);
  });

  it("/workflows lands on the Workflows list and keeps ?project", async () => {
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
    fireEvent.click(screen.getByRole("button", { name: "Back to app" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
  });

  it("Escape on a detail page steps back to its list", async () => {
    const router = renderAt("/workflows/runs/chat-1");
    await screen.findByTestId("page-run-detail");
    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/runs"));
  });

  it("the tabs carry the scope along with the project", async () => {
    renderAt("/workflows/library?project=%22proj-1%22&allProjects=true");
    await screen.findByTestId("page-library");
    const runs = within(screen.getByRole("navigation", { name: "Workflows" })).getByRole("link", { name: "Runs" });
    expect(runs).toHaveAttribute("href", "/workflows/runs?project=proj-1&allProjects=true");
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
    fireEvent.click(screen.getByTestId("workflows-project-switcher"));
    expect(await screen.findByRole("menu", { name: "Projects" })).toBeInTheDocument();
    // As a real keypress does: dispatched at the focused element, bubbling to document and window.
    fireEvent.keyDown(document.body, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("menu", { name: "Projects" })).toBeNull());
    expect(router.state.location.pathname).toBe("/workflows/library");
  });

  it.each([
    ["library", "Workflows"],
    ["runs", "Runs"],
  ])("the %s header shows its section and project scope", async (path, label) => {
    renderAt(`/workflows/${path}`);
    await screen.findByTestId(`page-${path}`);
    const header = screen.getByRole("banner");
    const breadcrumb = within(header).getByRole("navigation", { name: "Breadcrumb" });
    expect(breadcrumb).toHaveTextContent(`Workflows›${label}`);
    expect(within(header).queryByTestId("workflows-close")).not.toBeInTheDocument();
    const switcher = within(header).getByTestId("workflows-project-switcher");
    expect(switcher).toHaveTextContent("Reliant");
    expect(breadcrumb.compareDocumentPosition(switcher) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it.each([false, true])("keeps the exit in the brand row (collapsed: %s) and leaves the area", async (collapsed) => {
    const router = renderAt("/workflows/runs/chat-1?project=%22proj-1%22");
    await screen.findByTestId("page-run-detail");
    if (collapsed) fireEvent.click(screen.getByRole("button", { name: "Collapse sidebar" }));
    const brand = screen.getByTestId(collapsed ? "workflows-brand-collapsed" : "workflows-brand");
    const exit = within(brand).getByRole("button", { name: "Back to app" });
    expect(exit).toHaveAttribute("data-testid", "workflows-close");
    expect(exit.textContent).toBe("");
    expect(brand.closest("aside")).not.toBeNull();
    expect(screen.getAllByTestId("workflows-close")).toHaveLength(1);
    const nav = screen.getByRole("navigation", { name: "Workflows" });
    expect(within(nav).getByRole("link", { name: "Workflows" })).toHaveAttribute("href", "/workflows/library?project=proj-1");
    fireEvent.click(exit);
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    expect(router.state.location.search).toEqual({});
  });

  it("preserves native traffic-light spacing and non-draggable Electron controls, including on the rail", async () => {
    chrome.isElectron = true;
    chrome.trafficLightPadding = "80px";
    const router = renderAt("/workflows/library");
    await screen.findByTestId("page-library");
    const brand = screen.getByTestId("workflows-brand");
    expect(brand).toHaveStyle({ paddingLeft: "80px" });
    expect(brand.style.getPropertyValue("-webkit-app-region") || (brand.style as unknown as { WebkitAppRegion: string }).WebkitAppRegion).toBe("drag");
    const exit = within(brand).getByRole("button", { name: "Close workflows" });
    expect((exit.style as unknown as { WebkitAppRegion: string }).WebkitAppRegion).toBe("no-drag");
    fireEvent.click(screen.getByRole("button", { name: "Collapse sidebar" }));
    expect(screen.getByRole("button", { name: "Expand sidebar" })).toBeInTheDocument();
    expect(within(screen.getByTestId("workflows-brand-collapsed")).queryByTestId("workflows-close")).toBeNull();
    const headerExit = within(screen.getByRole("banner")).getByRole("button", { name: "Close workflows" });
    expect((headerExit.style as unknown as { WebkitAppRegion: string }).WebkitAppRegion).toBe("no-drag");
    fireEvent.click(headerExit);
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
  });

  it("offers a leading mobile exit with the drawer closed; Escape dismisses an open drawer only", async () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })));
    const router = renderAt("/workflows/library");
    await screen.findByTestId("page-library");
    const header = screen.getByRole("banner");
    const exit = within(header).getByRole("button", { name: "Back to app" });
    const breadcrumb = within(header).getByRole("navigation", { name: "Breadcrumb" });
    expect(exit.compareDocumentPosition(breadcrumb) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));
    expect(screen.getByRole("dialog", { name: "Workflows" })).toBeInTheDocument();
    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Workflows" })).toBeNull();
    expect(router.state.location.pathname).toBe("/workflows/library");
    fireEvent.click(exit);
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
  });

  it("the project menu is in the header on every page, and offers All projects", async () => {
    const router = renderAt("/workflows/runs");
    const switcher = await screen.findByTestId("workflows-project-switcher");
    expect(switcher).toHaveTextContent("Reliant");
    fireEvent.click(switcher);
    const menu = await screen.findByRole("menu", { name: "Projects" });
    fireEvent.click(within(menu).getByRole("menuitemradio", { name: /All projects/ }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ allProjects: true }));
    expect(await screen.findByTestId("workflows-project-switcher")).toHaveTextContent("All projects");
  });
});

describe("Retired paths redirect and keep their search params", () => {
  it.each([
    ["/workflow", "/workflows/library", "library"],
    ["/runs", "/workflows/runs", "runs"],
    ["/runs/chat-7", "/workflows/runs/chat-7", "run-detail"],
    ["/automations", "/workflows/library", "library"],
    ["/workflows/automations", "/workflows/library", "library"],
    ["/automations/trig-3", "/workflows/automations/trig-3", "automation-redirect"],
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

  it("the Automations tab lands on what runs by itself, in every project", async () => {
    const router = renderAt("/workflows/automations?project=%22proj-1%22");
    await screen.findByTestId("page-library");
    expect(router.state.location.search).toMatchObject({ show: "on", allProjects: true, project: "proj-1" });
  });

  it("carries ?tour across the hub redirect", async () => {
    const router = renderAt("/workflow?tour=%22workflow-hub%22");
    await screen.findByTestId("page-library");
    expect(router.state.location.search).toMatchObject({ tour: "workflow-hub" });
  });

  it("replaces history, so Back does not bounce through the old path", async () => {
    const router = renderAt("/automations/trig-3");
    await screen.findByTestId("page-automation-redirect");
    expect(router.history.length).toBe(1);
  });

  it("leaves the builder's /workflow/$workflowName alone", async () => {
    const router = renderAt("/workflow/builtin%3A%2F%2Fagent");
    expect(await screen.findByTestId("page-builder")).toBeInTheDocument();
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflow/builtin%3A%2F%2Fagent"));
  });
});
