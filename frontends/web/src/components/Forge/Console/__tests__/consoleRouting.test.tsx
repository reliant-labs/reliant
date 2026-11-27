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
import { QueryClientProvider, skipToken, useQuery } from "@tanstack/react-query";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { ObservabilityFixtures } from "@/test/observabilityFixtures";
import { listProjectsResponse } from "@/test/projectList";
import userEvent from "@testing-library/user-event";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useLocation,
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

vi.mock("@/store/authStore", async () => {
  const { identityOf } = await import("@/lib/auth/identity");
  const user = { id: "user-1", email: "ada@example.com", user_metadata: {} };
  // The real store derives identity from user; one object, as there.
  const state = { user, identity: identityOf(user as never), session: { access_token: "t" } };
  return { useAuthStore: (selector: (s: unknown) => unknown) => selector(state) };
});

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
const roster = vi.hoisted(() => ({ calls: [] as unknown[][], prodHolds: [] as unknown[], load: undefined as (() => Promise<boolean>) | undefined }));
vi.mock("@/hooks/forge-queries", () => ({
  useForgeRoster: (...args: unknown[]) => {
    roster.calls.push(args);
    const lookup = useQuery({ queryKey: ["test-roster", args[1]], queryFn: roster.load ?? skipToken });
    const live = (name: string, kind: string, holds: unknown[] = []) => ({ id: `env-${name}`, name, kind, holds });
    return {
      envs: lookup.isLoading ? [] : [
        { name: "prod", lifecycle: "deployed", source: "backend", live: live("prod", "persistent", roster.prodHolds), forge: null },
        { name: "e2e", lifecycle: "deployed", source: "backend", live: live("e2e", "persistent"), forge: null },
        { name: "dev", lifecycle: "local", source: "backend", live: live("dev", "local"), forge: null },
        { name: "pr-7", lifecycle: "deployed", source: "backend", live: live("pr-7", "preview"), forge: null },
      ],
      live: { data: undefined },
      topology: { isLoading: false },
      projectName: { name: args[1] ?? null, source: "project" },
      isLoading: lookup.isLoading,
      resolvingName: false,
    };
  },
  useForgeTopology: (projectId: string | null) => ({
    isLoading: false,
    error: null,
    data: projectId === "project-c" ? { kind: "report", report: { project: "gamma" } } : undefined,
  }),
  useEnvironmentShapes: () => new Map(),
}));

// The app switcher's list (the Apps page's query) is covered on its own.
vi.mock("@/hooks/apps-queries", () => ({
  useApps: () => ({ apps: [], loading: false, availability: "available", detail: "" }),
}));

vi.mock("@/hooks/observability-session-queries", () => ({ useSessions: () => ({ isSuccess: false }) }));
vi.mock("@/hooks/observability-queries", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/hooks/observability-queries")>(),
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
  // The open dashboard's own document, which the layout reads for its scope.
  useDashboard: () => ({ data: undefined }),
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

import { ShellFrameContext } from "@/components/Layout/shell/shellFrame";

import { ConsoleLayout } from "../ConsoleLayout";
import { useForgeConsole } from "../consoleContext";
import { useConsoleNavModel } from "../useConsoleNavModel";
import {
  ForgeLandingRoute,
  LegacyCommandRoute,
  LegacyDomainsRoute,
  LegacyEnvRoute,
  OrgObservabilityRoute,
} from "../ForgeEntryRoutes";
import { ForgeLayout } from "../../ForgeLayout";
import { createFixtureApi } from "../../Observability/__fixtures__/fixtureApi";
import { MetricsExplorerPage } from "../../Observability/Metrics/MetricsExplorerPage";

vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());

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

/** The page, and the nav's model for it, read as the shell's nav reads it (ShellNavBody NavWithConsole). */
function NavModelProbe() {
  const { forgeProject } = useForgeConsole();
  const { pathname, search } = useLocation();
  const model = useConsoleNavModel(forgeProject, pathname, search as { env?: unknown; project?: unknown });
  return <div data-testid="nav-model" data-alerts-firing={model.alertsFiring} />;
}

function renderAt(
  initialEntry: string,
  options: { onObsCall?: (rpc: string, request: unknown) => void; metrics?: boolean; inShell?: boolean; navModel?: boolean } = {},
) {
  const inShell = options.inShell ?? false;
  const root = createRootRoute({ component: () => <Outlet /> });
  const authenticated = createRoute({ getParentRoute: () => root, id: "_authenticated", component: () => <Outlet /> });
  const forge = createForgeConsoleRoutes(() => authenticated, {
    layout: ForgeLayout,
    console: ConsoleLayout,
    overview: probe("overview"),
    workload: probe("workload"),
    releases: probe("releases"),
    release: probe("release"),
    observabilityView: options.metrics ? MetricsExplorerPage : options.navModel ? NavModelProbe : probe("observability-view"),
    obsOverview: probe("obs-overview"),
    dashboards: probe("dashboards"),
    dashboard: probe("dashboard"),
    storage: probe("storage"),
    data: probe("data"),
    auth: probe("auth"),
    vulnerabilities: probe("vulnerabilities"),
    vulnerability: probe("vulnerability"),
    sboms: probe("sboms"),
    tests: probe("tests"),
    testsCoverage: probe("tests-coverage"),
    testsRuns: probe("tests-runs"),
    testsRun: probe("tests-run"),
    testsFlaky: probe("tests-flaky"),
    testsFlakyTest: probe("tests-flaky-test"),
    environments: probe("environments"),
    environment: probe("environment"),
    secrets: probe("secrets"),
    domains: probe("domains"),
    activity: probe("activity"),
    services: probe("services"),
    service: probe("service"),
    usage: probe("usage"),
    events: probe("events"),
    analytics: probe("analytics"),
    speedInsights: probe("speed-insights"),
    experiments: probe("experiments"),
    experiment: probe("experiment"),
    orgLayout: OrgLayoutStub,
    orgObservability: OrgObservabilityRoute,
    landing: ForgeLandingRoute,
    legacyEnv: LegacyEnvRoute,
    legacyDomains: LegacyDomainsRoute,
    legacyCommand: LegacyCommandRoute,
  });
  const home = createRoute({ getParentRoute: () => root, path: "/", component: () => <div data-testid="home">home</div> });
  const apps = createRoute({ getParentRoute: () => root, path: "/projects", component: () => <div data-testid="apps">apps</div> });
  const router = createRouter({
    routeTree: root.addChildren([authenticated.addChildren([forge]), home, apps]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  });
  render(
    <QueryClientProvider client={queryClient}>
      <ObservabilityFixtures api={createFixtureApi({ onCall: options.onObsCall })}>
        <ShellFrameContext.Provider value={{ inShell, navDocked: inShell }}>
          <RouterProvider router={router} />
        </ShellFrameContext.Provider>
      </ObservabilityFixtures>
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
  roster.load = undefined;
  window.localStorage.clear();
  vi.clearAllMocks();
});

afterEach(() => {
  queryClient.clear();
  setTransport(null);
});

describe("the way in: /forge", () => {
  it("names no project, so lands on the apps (Projects) rather than guessing one", async () => {
    // The persisted last project used to be opened here; the list of every
    // app replaced that guess (components/Apps/AppsPage.tsx).
    state.lastProjectId = "project-b";
    const router = renderAt("/forge");
    await landsOn(router, "/projects", { show: "apps" });
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
    await user.click(await screen.findByTestId("project-selector-row-project-a"));
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

describe("the project Metrics catalog", () => {
  it("starts the exact All catalog while both lookups are pending and reuses it in the explorer", async () => {
    let finishProjects!: (result: ReturnType<typeof listProjectsResponse>) => void;
    const projects = new Promise<ReturnType<typeof listProjectsResponse>>((resolve) => { finishProjects = resolve; });
    const listProjects = vi.fn(() => projects);
    setTransport(mockTransport({ "ProjectService.ListProjects": listProjects }));
    let finishRoster!: (result: boolean) => void;
    const live = new Promise<boolean>((resolve) => { finishRoster = resolve; });
    const loadRoster = vi.fn(() => live);
    roster.load = loadRoster;
    const onObsCall = vi.fn();

    renderAt("/forge/p/barkshop/observability/metrics?env=all&range=6h&f=%5B%22status%3Aok%22%5D", { metrics: true, onObsCall });
    await waitFor(() => expect(onObsCall).toHaveBeenCalledWith("ListMetrics", expect.objectContaining({
      orgId: "org-1",
      projectId: "barkshop",
      envIds: [],
      // Decoded off the wire, as the server reads it: an eq filter carries no value list.
      filters: [{ key: "status", op: "eq", value: "ok", values: [] }],
    })));
    const request = onObsCall.mock.calls.find(([rpc]) => rpc === "ListMetrics")?.[1];
    expect(request.window.endMs - request.window.startMs).toBe(6 * 60 * 60_000);
    expect(listProjects).toHaveBeenCalled();
    expect(loadRoster).toHaveBeenCalled();
    expect(screen.getByTestId("forge-console-loading")).toBeInTheDocument();

    finishProjects(listProjectsResponse(PROJECTS));
    expect(await screen.findByTestId("metrics-explorer-loading")).toBeInTheDocument();
    finishRoster(true);
    expect(await screen.findByTestId("metrics-catalog-list")).toBeInTheDocument();
    expect(onObsCall.mock.calls.filter(([rpc]) => rpc === "ListMetrics")).toHaveLength(1);
  });

  it("can start a named catalog with its recorded ID before the project list answers", async () => {
    let finishProjects!: (result: ReturnType<typeof listProjectsResponse>) => void;
    const projects = new Promise<ReturnType<typeof listProjectsResponse>>((resolve) => { finishProjects = resolve; });
    setTransport(mockTransport({ "ProjectService.ListProjects": () => projects }));
    const onObsCall = vi.fn();

    renderAt("/forge/p/barkshop/observability/metrics", { metrics: true, onObsCall });
    await waitFor(() => expect(onObsCall).toHaveBeenCalledWith("ListMetrics", expect.objectContaining({
      orgId: "org-1", projectId: "barkshop", envIds: ["env-prod"],
    })));
    expect(screen.getByTestId("forge-console-loading")).toBeInTheDocument();
    finishProjects(listProjectsResponse(PROJECTS));
    expect(await screen.findByTestId("metrics-catalog-list")).toBeInTheDocument();
    expect(onObsCall.mock.calls.filter(([rpc]) => rpc === "ListMetrics")).toHaveLength(1);
  });

  it.each(["prod", "dev"])("does not broaden a pending named environment (%s) to All", async (env) => {
    let finishRoster!: (result: boolean) => void;
    const live = new Promise<boolean>((resolve) => { finishRoster = resolve; });
    roster.load = () => live;
    const onObsCall = vi.fn();

    renderAt(`/forge/p/barkshop/observability/metrics?env=${env}`, { metrics: true, onObsCall });
    expect(await screen.findByTestId("metrics-explorer-loading")).toBeInTheDocument();
    expect(onObsCall).not.toHaveBeenCalledWith("ListMetrics", expect.anything());
    finishRoster(true);
    if (env === "dev") {
      expect(await screen.findByTestId("metrics-local-env")).toBeInTheDocument();
      expect(onObsCall).not.toHaveBeenCalledWith("ListMetrics", expect.anything());
    } else {
      expect(await screen.findByTestId("metrics-catalog-list")).toBeInTheDocument();
      expect(onObsCall.mock.calls.filter(([rpc]) => rpc === "ListMetrics")).toEqual([
        ["ListMetrics", expect.objectContaining({ orgId: "org-1", projectId: "barkshop", envIds: ["env-prod"] })],
      ]);
    }
  });
});

describe("redirects from the retired URLs", () => {
  it("sends a control-plane link's env tab to the page it means, scoped to that env", async () => {
    state.currentProject = PROJECTS[0]!; // Alpha is open; the link is about Beta's "barkshop".
    const router = renderAt("/forge/env/e2e?forgeProject=barkshop&tab=releases");
    await landsOn(router, "/forge/p/barkshop/releases", { env: "e2e" });
    // The console selects the project that declares it, over the current one.
    await waitFor(() => expect(selectProject).toHaveBeenCalledWith(expect.objectContaining({ id: "project-b" }), { skipWorkspaceStateSave: true }));
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
    await landsOn(router, "/forge/p/alpha/observability/logs", { env: "e2e", q: "timeout", project: "project-a" });
  });

  it("sends a retired secrets URL to that env's secret", async () => {
    state.currentProject = PROJECTS[0]!;
    const router = renderAt("/forge/secrets?env=prod&secret=DATABASE_URL");
    await landsOn(router, "/forge/p/alpha/secrets", { env: "prod", secret: "DATABASE_URL", project: "project-a" });
  });

  it("sends /forge/domains to Configure › Domains", async () => {
    state.currentProject = PROJECTS[0]!;
    const router = renderAt("/forge/domains");
    // The console names the checkout the entry resolved (consoleHost.ts).
    await landsOn(router, "/forge/p/alpha/domains", { env: "prod", project: "project-a" });
  });

  it("lands a view that is not a nav entry on the first view, Dashboards", async () => {
    const router = renderAt("/forge/p/alpha/observability/heatmaps?env=e2e");
    await landsOn(router, "/forge/p/alpha/observability/dashboards", { env: "e2e" });
  });

  it("opens Observability's own Overview at /observability (U6e)", async () => {
    const router = renderAt("/forge/p/alpha/observability?env=e2e");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "obs-overview");
    await landsOn(router, "/forge/p/alpha/observability", { env: "e2e" });
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

  it("remembers what the reader picks", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge/p/alpha/releases?env=prod");
    await screen.findByTestId("page");
    await user.click(screen.getByTestId("env-scope-all"));
    await landsOn(router, "/forge/p/alpha/releases", { env: "all" });
    expect(window.localStorage.getItem("forge.console.env.alpha")).toBe("all");
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

  it("routes the storage browser, keeping its view in the URL as text", async () => {
    const router = renderAt(
      `/forge/p/alpha/storage?env=e2e&bucket=uploads&prefix=${encodeURIComponent('"2026/"')}&object=${encodeURIComponent('"2026/a b%#?.png"')}&token=opaque-1`
    );
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "storage");
    await landsOn(router, "/forge/p/alpha/storage", { env: "e2e", bucket: "uploads", prefix: "2026/", object: "2026/a b%#?.png", token: "opaque-1" });
  });

  it("routes Auth's tabs, and sends the old Users URL to Auth with its search", async () => {
    const router = renderAt("/forge/p/alpha/auth/sign-in?env=e2e");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "auth");
    await landsOn(router, "/forge/p/alpha/auth/sign-in", { env: "e2e" });

    const redirected = renderAt("/forge/p/alpha/users?env=e2e&user=u-1");
    await landsOn(redirected, "/forge/p/alpha/auth", { env: "e2e", user: "u-1" });
  });

  it("routes Security › Vulnerabilities with its filters in the URL", async () => {
    const router = renderAt("/forge/p/alpha/security/vulnerabilities?env=e2e&severity=high&fixable=yes&kev=1&exempt=1&release=v1.4.0");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "vulnerabilities");
    await landsOn(router, "/forge/p/alpha/security/vulnerabilities", { env: "e2e", severity: "high", fixable: "yes", kev: "1", exempt: "1", release: "v1.4.0" });
  });

  it("routes one advisory, and lands /security on Vulnerabilities", async () => {
    const router = renderAt("/forge/p/alpha/security/vulnerabilities/CVE-2026-4101?env=e2e");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "vulnerability");
    await landsOn(router, "/forge/p/alpha/security/vulnerabilities/CVE-2026-4101", { env: "e2e" });

    const redirected = renderAt("/forge/p/alpha/security?env=e2e");
    await landsOn(redirected, "/forge/p/alpha/security/vulnerabilities", { env: "e2e" });
  });

  it("routes Security › SBOMs", async () => {
    const router = renderAt("/forge/p/alpha/security/sboms?env=e2e&release=v1.5.0");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "sboms");
    await landsOn(router, "/forge/p/alpha/security/sboms", { env: "e2e", release: "v1.5.0" });
  });

  it.each([
    ["/forge/p/alpha/tests?env=e2e&branch=main", "tests", "/forge/p/alpha/tests", { env: "e2e", branch: "main" }],
    [
      "/forge/p/alpha/tests/coverage?env=e2e&path=internal/billing/refund.go&commit=9f3c&lane=unit&base=77e0c1d",
      "tests-coverage",
      "/forge/p/alpha/tests/coverage",
      { env: "e2e", path: "internal/billing/refund.go", commit: "9f3c", lane: "unit", base: "77e0c1d" },
    ],
    ["/forge/p/alpha/tests/runs?env=e2e", "tests-runs", "/forge/p/alpha/tests/runs", { env: "e2e" }],
    ["/forge/p/alpha/tests/runs/01JD7Q?env=e2e", "tests-run", "/forge/p/alpha/tests/runs/01JD7Q", { env: "e2e" }],
    ["/forge/p/alpha/tests/flaky?env=e2e", "tests-flaky", "/forge/p/alpha/tests/flaky", { env: "e2e" }],
    ["/forge/p/alpha/tests/flaky/dGVzdA?env=e2e", "tests-flaky-test", "/forge/p/alpha/tests/flaky/dGVzdA", { env: "e2e" }],
  ])("routes the Tests page %s", async (url, page, path, search) => {
    const router = renderAt(url);
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", page);
    await landsOn(router, path, search);
    // The Tests pages narrow by their own `environment`: the bar draws no env picker (useRouteUsesEnvScope).
    expect(router.state.matches.some((match) => match.staticData?.envScope === false)).toBe(true);
  });

  it("keeps the env scope on the pages that read it", async () => {
    const router = renderAt("/forge/p/alpha/releases?env=e2e");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "releases");
    expect(router.state.matches.some((match) => match.staticData?.envScope === false)).toBe(false);
  });

  it("reads a numeric-looking key back as the text it is", async () => {
    const router = renderAt("/forge/p/alpha/storage?env=e2e&bucket=uploads&prefix=2026");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "storage");
    await landsOn(router, "/forge/p/alpha/storage", { env: "e2e", bucket: "uploads", prefix: "2026" });
  });

  it("routes the data browser, keeping its view in the URL", async () => {
    const router = renderAt(
      "/forge/p/alpha/data?env=e2e&db=shop&table=public.orders&f=%5B%22status.eq.paid%22%5D&sort=%5B%22id.desc%22%5D&cols=%5B%22id%22%5D&page=2&row=%221042%22"
    );
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "data");
    await landsOn(router, "/forge/p/alpha/data", {
      env: "e2e",
      db: "shop",
      table: "public.orders",
      f: ["status.eq.paid"],
      sort: ["id.desc"],
      cols: ["id"],
      page: 2,
      row: "1042",
    });
  });

  it("is left alone on a page that pins its own environment", async () => {
    const router = renderAt("/forge/p/alpha/environments/dev");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "environment");
    expect(screen.getByTestId("env-scope")).toHaveAttribute("data-pinned", "true");
    expect(router.state.location.search).toEqual({});
  });
});

// The nav is the shell's (Layout/sidebar/navConfig, its own tests); what the
// console hands it is the model (useConsoleNavModel), read here as the shell's
// nav reads it.
describe("the nav's model", () => {
  it("counts the alerts firing in scope, for the badge on Alerts", async () => {
    renderAt("/forge/p/alpha/observability/alerts?env=all", { navModel: true });
    // prod and e2e fire; another project's env and a pending alert do not count.
    await waitFor(() => expect(screen.getByTestId("nav-model")).toHaveAttribute("data-alerts-firing", "2"));
  });

  it("names the open dashboard in the breadcrumb", async () => {
    renderAt("/forge/p/alpha/observability/dashboards/workspace/checkout-latency?env=prod");
    expect(await screen.findByTestId("page")).toHaveAttribute("data-page", "dashboard");
    await waitFor(() => expect(screen.getByRole("navigation", { name: /breadcrumb/i })).toHaveTextContent("Checkout latency"));
  });
});

describe("leaving the console", () => {
  it("closes to / on Escape", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge/p/alpha?env=prod");
    await screen.findByTestId("page");
    await user.keyboard("{Escape}");
    await landsOn(router, "/");
  });
});

// U6: in the product the console renders inside the one shell, whose left nav
// draws the app's sections (Layout/sidebar/navConfig). The console then
// has NO nav column of its own: a slim bar with the breadcrumb and the one
// environment selector, over the page.
describe("inside the one shell", () => {
  it("draws no nav column, only the breadcrumb and the scope over the page", async () => {
    renderAt("/forge/p/alpha/observability/errors?env=prod", { inShell: true });
    const page = await screen.findByTestId("page");
    expect(page).toHaveAttribute("data-page", "observability-view");
    expect(page).toHaveAttribute("data-env", "prod");
    expect(document.querySelector("aside")).toBeNull();
    expect(screen.queryByRole("navigation", { name: "alpha" })).toBeNull();
    expect(screen.queryByTestId("forge-shell")).toBeNull();
    const frame = screen.getByTestId("forge-console-frame");
    expect(frame.className).toContain("forge-ui");
    const header = screen.getByTestId("forge-console-header");
    expect(within(header).getByRole("navigation", { name: /breadcrumb/i })).toHaveTextContent("alpha›Performance & issues›Errors");
    // Inside the shell the frame (AppShellFrame) is the one main landmark: the console's body is a plain region.
    expect(within(frame).queryByRole("main")).toBeNull();
    expect(frame).toContainElement(page);
  });

  it("still defaults the scope and reflects it into the URL", async () => {
    const router = renderAt("/forge/p/alpha/releases", { inShell: true });
    await landsOn(router, "/forge/p/alpha/releases", { env: "prod" });
  });
});

describe("several checkouts declare one app: ?project= names which", () => {
  const TWO_CHECKOUTS: TestProject[] = [
    ...PROJECTS,
    { id: "project-b2", name: "Beta, second checkout", path: "/src/beta-2", forge_project_name: "barkshop" },
  ];
  beforeEach(() => {
    setTransport(mockTransport({ "ProjectService.ListProjects": () => listProjectsResponse(TWO_CHECKOUTS) }));
  });

  it("asks which, rather than opening the first, when the URL names none", async () => {
    // Not even the current project decides: it is the tab's history, not the URL.
    state.currentProject = PROJECTS[1]!;
    renderAt("/forge/p/barkshop");
    expect(await screen.findByTestId("console-checkout-picker")).toBeInTheDocument();
    expect(screen.getByTestId("console-checkout-project-b")).toBeInTheDocument();
    expect(screen.getByTestId("console-checkout-project-b2")).toBeInTheDocument();
    expect(screen.queryByTestId("page")).toBeNull();
    expect(selectProject).not.toHaveBeenCalled();
  });

  it("opens the checkout the person picks, and names it in the URL", async () => {
    const user = userEvent.setup();
    const router = renderAt("/forge/p/barkshop");
    await user.click(await screen.findByTestId("console-checkout-project-b2"));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ project: "project-b2" }));
    expect((await screen.findByTestId("page")).dataset.projectId).toBe("project-b2");
    expect(selectProject).toHaveBeenCalledWith(expect.objectContaining({ id: "project-b2" }), { skipWorkspaceStateSave: true });
  });

  it("opens the checkout ?project= names", async () => {
    renderAt("/forge/p/barkshop?project=project-b2");
    expect((await screen.findByTestId("page")).dataset.projectId).toBe("project-b2");
    expect(screen.queryByTestId("console-checkout-picker")).toBeNull();
  });

  it("keeps ?project= across the console's own links", async () => {
    const router = renderAt("/forge/p/barkshop?project=project-b2");
    await screen.findByTestId("page");
    await router.navigate({ href: "/forge/p/barkshop/releases?env=prod" });
    await landsOn(router, "/forge/p/barkshop/releases");
    expect(router.state.location.search).toMatchObject({ project: "project-b2", env: "prod" });
    expect((await screen.findByTestId("page")).dataset.projectId).toBe("project-b2");
  });

  it("names the checkout an old /forge?project= link resolved", async () => {
    const router = renderAt("/forge?project=project-b2");
    await landsOn(router, "/forge/p/barkshop");
    await waitFor(() => expect(router.state.location.search).toMatchObject({ project: "project-b2" }));
    expect((await screen.findByTestId("page")).dataset.projectId).toBe("project-b2");
  });
});
