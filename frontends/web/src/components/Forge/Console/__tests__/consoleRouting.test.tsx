// Copyright (c) 2025 Reliant Labs

/**
 * The console's routes, end to end through the REAL route tree
 * (forgeConsoleRoutes) with probe pages:
 *
 *   - the bare /forge and /forge/observability are the org's pages (stubbed
 *     here), and name no project; every way in that does name or imply one —
 *     `?project=`, `?forgeProject=`, the retired URLs a notification or
 *     bookmark carries — resolves it and lands on the page it means, scope
 *     included;
 *   - the console looks the Reliant project up from the forge project in its
 *     path, and a name no project here declares still opens (read from the
 *     control plane alone, no daemon);
 *   - the environment scope is defaulted, remembered per project and
 *     reflected into `?env=` on list pages, and left alone on a page that pins
 *     its own;
 *   - Escape and the close button leave.
 *
 * The project selection is mocked as a real, mutating store rather than a
 * frozen snapshot: resolution is async and reads back through getState()
 * after awaiting, so a mock that could not change would pass while the real
 * thing deadlocked. The project LIST is the real one: ListProjects answered
 * at the transport, read through the app's query client.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { listProjectsResponse } from "@/test/projectList";
import userEvent from "@testing-library/user-event";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  type RouteComponent,
} from "@tanstack/react-router";

interface TestProject {
  id: string;
  name: string;
  path: string;
  forge_project_name?: string;
}

const PROJECTS: TestProject[] = [
  { id: "project-a", name: "Alpha", path: "/src/alpha", forge_project_name: "alpha" },
  { id: "project-b", name: "Beta", path: "/src/beta", forge_project_name: "barkshop" },
  { id: "project-c", name: "Gamma", path: "/src/gamma" },
];

const state: { currentProject: TestProject | null; lastProjectId: string | null } = {
  currentProject: null,
  lastProjectId: null,
};

const selectProject = vi.fn(async (project: TestProject) => {
  state.currentProject = project;
});
const restoreLastProject = vi.fn(async () => {
  const match = PROJECTS.find((p) => p.id === state.lastProjectId);
  if (!match) return false;
  state.currentProject = match;
  return true;
});

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({
    currentProject: state.currentProject,
    selectProject,
    restoreLastProject,
  });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => (selector ? selector(snapshot()) : snapshot()),
    { getState: snapshot, setState: vi.fn(), subscribe: vi.fn(() => () => undefined) }
  );
  return { useProjectStore };
});

vi.mock("@/store/authStore", () => ({
  useAuthStore: (selector: (s: unknown) => unknown) => selector({ user: { email: "ada@example.com", user_metadata: {} }, session: { access_token: "t" } }),
}));

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

// The roster's data layer is covered on its own (services/forge/__tests__/
// roster.test.ts); here it names the environments and records the scope it
// was asked for. A project with no persisted name reports one through its
// daemon's topology.
const roster = vi.hoisted(() => ({ calls: [] as unknown[][], prodHolds: [] as unknown[] }));
vi.mock("@/hooks/forge-queries", () => ({
  useForgeRoster: (...args: unknown[]) => {
    roster.calls.push(args);
    const live = (name: string, kind: string, holds: unknown[] = []) => ({ id: `env-${name}`, name, kind, holds });
    return {
      envs: [
        { name: "prod", lifecycle: "deployed", source: "backend", live: live("prod", "persistent", roster.prodHolds), forge: null },
        { name: "e2e", lifecycle: "deployed", source: "backend", live: live("e2e", "persistent"), forge: null },
        { name: "dev", lifecycle: "local", source: "backend", live: live("dev", "local"), forge: null },
        { name: "pr-7", lifecycle: "deployed", source: "backend", live: live("pr-7", "preview"), forge: null },
      ],
      live: { data: undefined },
      topology: { isLoading: false },
      projectName: { name: args[1] ?? null, source: "project" },
      isLoading: false,
      resolvingName: false,
    };
  },
  useForgeTopology: (projectId: string | null) => ({
    isLoading: false,
    error: null,
    data: projectId === "project-c" ? { kind: "report", report: { project: "gamma" } } : undefined,
  }),
}));

// The app switcher's list (the Apps page's query) is covered on its own.
vi.mock("@/hooks/apps-queries", () => ({
  useApps: () => ({ apps: [], loading: false, availability: "available", detail: "" }),
}));

vi.mock("@/hooks/observability-session-queries", () => ({ useSessions: () => ({ isSuccess: false }) }));
vi.mock("@/hooks/observability-queries", () => ({
  useServiceSignals: () => ({ isSuccess: false }),
  useObservabilityOrg: () => ({ orgId: "org-1", isLoading: false, error: null }),
  useAlerts: () => ({
    data: [
      { envId: "env-prod", state: "firing" },
      { envId: "env-e2e", state: "firing" },
      { envId: "env-elsewhere", state: "firing" },
      { envId: "env-prod", state: "pending" },
    ],
  }),
}));

// The index the nav reads its starred dashboards from (the Dashboards page shares it).
vi.mock("@/hooks/dashboard-queries", () => ({
  useDashboardIndex: () => ({
    data: {
      canWrite: true,
      dashboards: [
        { ref: { kind: "workspace", projectId: "shop", key: "checkout-latency" }, title: "Checkout latency", starred: true },
        { ref: { kind: "release", projectId: "shop", key: "workloads" }, title: "Workloads", starred: false },
      ],
    },
  }),
}));

// The org pages (slice D) are covered on their own; here they are stubs that
// say which one rendered, so the routes' choices are what is under test.
vi.mock("@/components/Forge/Org", async () => {
  return {
    OrgObservabilityPage: () => <div data-testid="org-page" data-page="org-observability" />,
  };
});

import { createForgeConsoleRoutes } from "@/forgeConsoleRoutes";

import { ConsoleLayout } from "../ConsoleLayout";
import { useForgeConsole } from "../consoleContext";
import {
  ForgeLandingRoute,
  LegacyCommandRoute,
  LegacyDomainsRoute,
  LegacyEnvRoute,
  OrgObservabilityRoute,
} from "../ForgeEntryRoutes";
import { ForgeLayout } from "../../ForgeLayout";

/** Stands in for the org chrome around the org pages. */
function OrgLayoutStub() {
  return (
    <div data-testid="org-layout">
      <Outlet />
    </div>
  );
}

/** A page that says which page it is and what the console told it. */
function probe(name: string): RouteComponent {
  return function Probe() {
    const { forgeProject, projectId, env } = useForgeConsole();
    return (
      <div data-testid="page" data-page={name} data-project={forgeProject} data-project-id={projectId ?? ""} data-env={env}>
        {name}
      </div>
    );
  };
}

function renderAt(initialEntry: string) {
  const root = createRootRoute({ component: () => <Outlet /> });
  const authenticated = createRoute({ getParentRoute: () => root, id: "_authenticated", component: () => <Outlet /> });
  const forge = createForgeConsoleRoutes(() => authenticated, {
    layout: ForgeLayout,
    console: ConsoleLayout,
    overview: probe("overview"),
    workload: probe("workload"),
    releases: probe("releases"),
    release: probe("release"),
    observabilityView: probe("observability-view"),
    dashboards: probe("dashboards"),
    dashboard: probe("dashboard"),
    environments: probe("environments"),
    environment: probe("environment"),
    secrets: probe("secrets"),
    domains: probe("domains"),
    activity: probe("activity"),
    orgLayout: OrgLayoutStub,
    orgObservability: OrgObservabilityRoute,
    landing: ForgeLandingRoute,
    legacyEnv: LegacyEnvRoute,
    legacyDomains: LegacyDomainsRoute,
    legacyCommand: LegacyCommandRoute,
  });
  const home = createRoute({ getParentRoute: () => root, path: "/", component: () => <div data-testid="home">home</div> });
  const apps = createRoute({ getParentRoute: () => root, path: "/apps", component: () => <div data-testid="apps">apps</div> });
  const router = createRouter({
    routeTree: root.addChildren([authenticated.addChildren([forge]), home, apps]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  });
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

async function landsOn(router: ReturnType<typeof renderAt>, pathname: string, search?: Record<string, unknown>) {
  await waitFor(() => expect(router.state.location.pathname).toBe(pathname));
  if (search) await waitFor(() => expect(router.state.location.search).toEqual(search));
}

beforeEach(() => {
  setTransport(mockTransport({ "ProjectService.ListProjects": () => listProjectsResponse(PROJECTS) }));
  state.currentProject = null;
  state.lastProjectId = null;
  roster.calls.length = 0;
  roster.prodHolds = [];
  window.localStorage.clear();
  vi.clearAllMocks();
});

afterEach(() => {
  queryClient.clear();
  setTransport(null);
});

describe("the way in: /forge", () => {
  it("names no project, so lands on the Apps list rather than guessing one", async () => {
    // The persisted last project used to be opened here; the list of every
    // app replaced that guess (components/Apps/AppsPage.tsx).
    state.lastProjectId = "project-b";
    const router = renderAt("/forge");
    await landsOn(router, "/apps");
    expect(await screen.findByTestId("apps")).toBeInTheDocument();
    expect(restoreLastProject).not.toHaveBeenCalled();
  });

  it("resolves a ?project= id (an old bookmark)", async () => {
    const router = renderAt("/forge?project=project-a");
    await landsOn(router, "/forge/p/alpha");
  });

  it("asks the daemon for the name of a project that has none persisted", async () => {
    const router = renderAt("/forge?project=project-c");
    await landsOn(router, "/forge/p/gamma");
  });

  it("opens the app a ?forgeProject= link names, with no lookup", async () => {
    const router = renderAt("/forge?forgeProject=elsewhere");
    await landsOn(router, "/forge/p/elsewhere");
    expect(screen.queryByTestId("forge-project-picker")).toBeNull();
  });

  it("offers a picker — never a dead-end sentence — when a ?project= resolves to nothing", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge?project=gone");
    await screen.findByTestId("forge-project-picker");
    await user.click(await screen.findByTestId("forge-project-option-project-a"));
    expect(selectProject).toHaveBeenCalledWith(expect.objectContaining({ id: "project-a" }));
    await landsOn(router, "/forge/p/alpha");
  });
});

describe("the org's Observability: /forge/observability", () => {
  it("is the org page — no project is resolved", async () => {
    state.currentProject = PROJECTS[0]!;
    const router = renderAt("/forge/observability");
    expect(await screen.findByTestId("org-page")).toHaveAttribute("data-page", "org-observability");
    expect(router.state.location.pathname).toBe("/forge/observability");
  });

  it("hosts a view at /forge/observability/<view>, keeping the filters", async () => {
    const router = renderAt("/forge/observability/logs?apps=%5B%22shop%22%5D&q=timeout");
    expect(await screen.findByTestId("org-page")).toHaveAttribute("data-page", "org-observability");
    await landsOn(router, "/forge/observability/logs", { apps: ["shop"], q: "timeout" });
  });

  it("lands a view that is not a nav entry on the org overview, keeping the filters", async () => {
    const router = renderAt("/forge/observability/heatmaps?apps=%5B%22shop%22%5D");
    await landsOn(router, "/forge/observability", { apps: ["shop"] });
  });

  it("sends a bare ?view= (an old link) to that view's org page, keeping the filters", async () => {
    const router = renderAt("/forge/observability?view=logs&envs=%5B%22shop%2Fprod%22%5D&apps=%5B%22shop%22%5D&q=timeout");
    await landsOn(router, "/forge/observability/logs", { envs: ["shop/prod"], apps: ["shop"], q: "timeout" });
  });

  it("sends the retired ?view=usage to the org overview", async () => {
    const router = renderAt("/forge/observability?view=usage");
    await landsOn(router, "/forge/observability", {});
  });

  it("resolves ?forgeProject= to that app's page for the view", async () => {
    const router = renderAt("/forge/observability?forgeProject=elsewhere&view=logs&q=timeout");
    await landsOn(router, "/forge/p/elsewhere/observability/logs", { env: "all", q: "timeout" });
  });
});

describe("redirects from the retired URLs", () => {
  it("sends a control-plane link's env tab to the page it means, scoped to that env", async () => {
    state.currentProject = PROJECTS[0]!; // Alpha is open; the link is about Beta's "barkshop".
    const router = renderAt("/forge/env/e2e?forgeProject=barkshop&tab=releases");
    await landsOn(router, "/forge/p/barkshop/releases", { env: "e2e" });
    // The console selects the project that declares it, over the current one.
    await waitFor(() => expect(selectProject).toHaveBeenCalledWith(expect.objectContaining({ id: "project-b" })));
    expect(screen.getByTestId("page")).toHaveAttribute("data-env", "e2e");
  });

  it("opens a forge project no Reliant project declares, with no daemon and no picker", async () => {
    const router = renderAt("/forge/env/prod?forgeProject=elsewhere");
    await landsOn(router, "/forge/p/elsewhere/environments/prod", { env: "prod" });
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "environment");
    expect(screen.getByTestId("page")).toHaveAttribute("data-project-id", "");
    expect(screen.queryByTestId("forge-project-picker")).toBeNull();
    expect(roster.calls.at(-1)).toEqual([null, "elsewhere"]);
  });

  it("sends /forge/observability?project= to the view's page with envs as the scope", async () => {
    const router = renderAt('/forge/observability?project=project-a&view=logs&envs=%5B%22e2e%22%5D&q=timeout');
    await landsOn(router, "/forge/p/alpha/observability/logs", { env: "e2e", q: "timeout" });
  });

  it("sends a retired secrets URL to that env's secret", async () => {
    state.currentProject = PROJECTS[0]!;
    const router = renderAt("/forge/secrets?env=prod&secret=DATABASE_URL");
    await landsOn(router, "/forge/p/alpha/secrets", { env: "prod", secret: "DATABASE_URL" });
  });

  it("sends /forge/domains to Configure › Domains", async () => {
    state.currentProject = PROJECTS[0]!;
    const router = renderAt("/forge/domains");
    await landsOn(router, "/forge/p/alpha/domains", { env: "prod" });
  });

  it("lands a view that is not a nav entry on the first view", async () => {
    const router = renderAt("/forge/p/alpha/observability/heatmaps?env=e2e");
    await landsOn(router, "/forge/p/alpha/observability/graph", { env: "e2e" });
  });

  it("lands the retired Observability home on the Overview, which carries its strip", async () => {
    const router = renderAt("/forge/p/alpha/observability?env=e2e");
    await landsOn(router, "/forge/p/alpha", { env: "e2e" });
  });

  it("lands the retired Monitoring settings on Alerts, where routing and silences live", async () => {
    const router = renderAt("/forge/p/alpha/monitoring?env=e2e");
    await landsOn(router, "/forge/p/alpha/observability/alerts", { env: "e2e" });
  });
});

describe("the environment scope", () => {
  it("defaults to prod and reflects it into the URL on a list page", async () => {
    const router = renderAt("/forge/p/alpha/releases");
    await landsOn(router, "/forge/p/alpha/releases", { env: "prod" });
    expect(screen.getByTestId("env-scope-prod")).toHaveAttribute("aria-pressed", "true");
  });

  it("opens on the scope remembered for the project", async () => {
    window.localStorage.setItem("forge.console.env.alpha", "e2e");
    const router = renderAt("/forge/p/alpha/activity");
    await landsOn(router, "/forge/p/alpha/activity", { env: "e2e" });
  });

  it("replaces an environment that does not exist with prod", async () => {
    const router = renderAt("/forge/p/alpha/secrets?env=gone");
    await landsOn(router, "/forge/p/alpha/secrets", { env: "prod" });
  });

  it("remembers what the reader picks, and carries it on every nav link", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge/p/alpha/releases?env=prod");
    await screen.findByTestId("page");
    await user.click(screen.getByTestId("env-scope-all"));
    await landsOn(router, "/forge/p/alpha/releases", { env: "all" });
    expect(window.localStorage.getItem("forge.console.env.alpha")).toBe("all");

    await user.click(screen.getByRole("link", { name: "Activity" }));
    await landsOn(router, "/forge/p/alpha/activity", { env: "all" });
  });

  it("collapses previews into the scope menu", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge/p/alpha/releases?env=prod");
    await screen.findByTestId("page");
    expect(screen.queryByTestId("env-scope-pr-7")).toBeNull();
    await user.click(screen.getByTestId("env-scope-more"));
    await user.click(within(screen.getByRole("menu")).getByRole("menuitemradio", { name: "pr-7" }));
    await landsOn(router, "/forge/p/alpha/releases", { env: "pr-7" });
  });

  it("is left alone on a page that pins its own environment", async () => {
    const router = renderAt("/forge/p/alpha/environments/dev");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "environment");
    expect(screen.getByTestId("env-scope")).toHaveAttribute("data-pinned", "true");
    expect(router.state.location.search).toEqual({});
  });
});

describe("the nav", () => {
  it("clears every graph-only search key when leaving Observability while preserving the environment scope", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge/p/alpha/observability/graph?env=e2e&declared=true&internal=true&rpcId=acme.v1.OrdersService%2FGetOrder&rpcService=acme.v1.OrdersService");
    await screen.findByTestId("page");
    expect(router.state.location.search).toMatchObject({ env: "e2e", declared: true, internal: true, rpcId: "acme.v1.OrdersService/GetOrder", rpcService: "acme.v1.OrdersService" });
    await user.click(screen.getByRole("link", { name: "Secrets" }));
    await landsOn(router, "/forge/p/alpha/secrets", { env: "e2e" });
    expect(router.state.location.search).toEqual({ env: "e2e" });
  });

  it("opens an environment's page from Configure › Environments", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge/p/alpha/environments?env=prod");
    await screen.findByTestId("page");
    await user.click(screen.getByRole("link", { name: "e2e" }));
    await landsOn(router, "/forge/p/alpha/environments/e2e");
    expect(screen.getByTestId("page")).toHaveAttribute("data-page", "environment");
  });

  it("nests the starred dashboards under Dashboards, and names the open one in the breadcrumb", async () => {
    renderAt("/forge/p/alpha/observability/dashboards/workspace/checkout-latency?env=prod");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "dashboard");
    const starred = screen.getByRole("link", { name: "Checkout latency" });
    expect(starred).toHaveAttribute("href", "/forge/p/alpha/observability/dashboards/workspace/checkout-latency?env=prod");
    expect(starred).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "All dashboards" })).toHaveAttribute("href", "/forge/p/alpha/observability/dashboards?env=prod");
    expect(screen.queryByRole("link", { name: "Workloads" })).not.toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: /breadcrumb/i })).toHaveTextContent("Checkout latency");
  });

  it("counts the alerts firing in scope on Alerts", async () => {
    renderAt("/forge/p/alpha/observability/alerts?env=all");
    await screen.findByTestId("page");
    // prod and e2e fire; another project's env and a pending alert do not count.
    expect(screen.getByRole("link", { name: /Alerts/ })).toHaveTextContent("2");
  });

  it("marks an environment whose deploy is queued", async () => {
    roster.prodHolds = [{ kind: "billing", promotionId: "p", reason: "", fix: "", actionUrl: "", callerCanResolve: false }];
    renderAt("/forge/p/alpha/environments");
    const mark = await screen.findByTestId("forge-nav-queued-prod");
    expect(mark).toHaveTextContent("waiting on billing");
  });
});

describe("leaving the console", () => {
  it("closes to / via the brand row's button", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge/p/alpha?env=prod");
    await user.click(await screen.findByTestId("forge-close"));
    await landsOn(router, "/");
  });

  it("closes to / on Escape", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge/p/alpha?env=prod");
    await screen.findByTestId("page");
    await user.keyboard("{Escape}");
    await landsOn(router, "/");
  });
});
