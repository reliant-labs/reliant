// Copyright (c) 2026 Reliant Labs

/**
 * A console page across several projects (owner, 2026-11-25: no "pick one
 * project" in their place): the route's Outlet once per project, each inside
 * that project's console context, under one title and the `?only=` filter.
 */

import { cleanup, render, screen, within } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import type { ManagedStoreTarget } from "@/services/forge/secretStore";

const { listSecrets } = vi.hoisted(() => ({
  listSecrets: vi.fn(async (environmentId: string | null) => ({
    secrets: [{ name: `${environmentId}-secret` }],
    protected: false,
  })),
}));

// Keep the real SecretsPage and SecretsMatrix, replacing only their I/O.
vi.mock("../../EnvPage/EnvTabPanels", () => ({ SecretsTab: () => null }));

// A console with a control plane: each live environment is a managed-store lookup.
vi.mock("@/services/controlPlane/config", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/config")>()),
  CONTROL_PLANE_API_URL: "https://cp.test",
  hasControlPlane: true,
}));

vi.mock("../consoleHost", () => ({
  useConsoleReliantProject: (app: string, options: { select?: boolean; pinned?: string | null }) => {
    // The page shows several projects: a section never selects one.
    if (options.select !== false) throw new Error("a section must not select its project");
    return { listed: true, projectId: `pid-${app}`, candidates: null };
  },
}));

vi.mock("@/hooks/forge-queries", async (importOriginal) => ({
  // The real managedSecretsOf reads each listing as the matrix does.
  managedSecretsOf: (await importOriginal<typeof import("@/hooks/forge-queries")>()).managedSecretsOf,
  managedSecretsQueryOptions: (env: string | null | undefined, target: ManagedStoreTarget | null) => {
    const environmentId = target?.kind === "lookup" ? target.environmentId : null;
    return { queryKey: ["section-secrets", environmentId, env], queryFn: () => listSecrets(environmentId) };
  },
  useEnvironmentShapes: () => new Map(),
  useForgeRoster: (projectId: string | null, app: string) => ({
    isLoading: false,
    resolvingName: false,
    topology: { isLoading: false },
    live: { data: { availability: "available" } },
    envs: app === "empty" ? [] : [{ name: "prod", lifecycle: "long", live: { id: `${app}-prod`, kind: "long" } }],
    projectId,
  }),
}));

vi.mock("@/fixtures/scope", () => ({ AppScopeProvider: ({ children }: { children: React.ReactNode }) => <>{children}</> }));

vi.mock("../../../Layout/sidebar/shellProject", () => ({
  useShellLastPick: (select: (state: { set: (key: string) => void }) => unknown) => select({ set: () => {} }),
  useShellSeveral: () => ({ mode: null, rows: [] }),
  sectionsAppOf: () => null,
}));

const { SeveralProjectSections } = await import("../severalProjectSections");
const { ForgeConsoleContext } = await import("../consoleContextValue");
const { useForgeConsole } = await import("../consoleContext");
const { Header } = await import("../consolePageFrame");
const { SecretsPage } = await import("../SecretsPage");
const { severalModeOf } = await import("../severalProjects");

const APPS = [
  { key: "p1", name: "Shop", app: "shop" },
  { key: "p2", name: "Blog", app: "blog" },
  { key: "p3", name: "Empty", app: "empty" },
];

/** A page that says which project its console context is about, under its own header. */
function SecretsProbe() {
  const { forgeProject, projectId, env, href } = useForgeConsole();
  return (
    <div data-testid={`probe-${forgeProject}`}>
      <Header title="Secrets" meta={<span>{`${forgeProject} meta`}</span>} />
      <span>{`${forgeProject}|${projectId}|${env}`}</span>
      <a href={href({ kind: "secrets" })}>link</a>
    </div>
  );
}

function renderAt(path: string, component = SecretsProbe) {
  const outer = {
    forgeProject: "shop",
    projectId: "pid-shop",
    page: { kind: "secrets" } as const,
    env: "prod",
    setEnv: () => {},
    roster: {} as never,
    href: () => "",
  };
  const root = createRootRoute({ component: () => <Outlet /> });
  const layout = createRoute({
    getParentRoute: () => root,
    path: "/forge/p/$project",
    component: () => (
      <ForgeConsoleContext.Provider value={outer}>
        <SeveralProjectSections apps={APPS}>
          <Outlet />
        </SeveralProjectSections>
      </ForgeConsoleContext.Provider>
    ),
  });
  const page = createRoute({ getParentRoute: () => layout, path: "secrets", component });
  const router = createRouter({ routeTree: root.addChildren([layout.addChildren([page])]), history: createMemoryHistory({ initialEntries: [path] }) });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

afterEach(cleanup);

describe("a console page across several projects", () => {
  it("runs the real routed Secrets page queries independently in each project scope", async () => {
    listSecrets.mockClear();
    renderAt("/forge/p/shop/secrets?env=all", SecretsPage);
    const shop = await screen.findByTestId("several-section-shop");
    const blog = screen.getByTestId("several-section-blog");
    // Each section's managed store is its own live environment's (shop-prod, blog-prod).
    expect(await within(shop).findByText("shop-prod-secret")).toBeInTheDocument();
    expect(await within(blog).findByText("blog-prod-secret")).toBeInTheDocument();
    expect(within(shop).queryByText("blog-prod-secret")).not.toBeInTheDocument();
    expect(within(blog).queryByText("shop-prod-secret")).not.toBeInTheDocument();
    expect(listSecrets.mock.calls.map(([id]) => id).sort()).toEqual(["blog-prod", "shop-prod"]);
    expect(within(shop).getByRole("link", { name: "prod" })).toHaveAttribute("href", "/forge/p/shop/secrets?env=prod");
    expect(within(blog).getByRole("link", { name: "prod" })).toHaveAttribute("href", "/forge/p/blog/secrets?env=prod");
  });

  it("renders the page once per project, each in its own project's console context", async () => {
    renderAt("/forge/p/shop/secrets?env=prod");
    const shop = await screen.findByTestId("several-section-shop");
    expect(within(shop).getByText("shop|pid-shop|prod")).toBeInTheDocument();
    expect(within(shop).getByRole("link", { name: "link" })).toHaveAttribute("href", "/forge/p/shop/secrets?env=prod");
    const blog = screen.getByTestId("several-section-blog");
    expect(within(blog).getByText("blog|pid-blog|prod")).toBeInTheDocument();
    expect(within(blog).getByRole("link", { name: "link" })).toHaveAttribute("href", "/forge/p/blog/secrets?env=prod");
    // An undeployed project says so, once, instead of the page's guide.
    expect(within(screen.getByTestId("several-section-empty")).getByTestId("several-section-undeployed-empty")).toHaveTextContent("Empty has nothing deployed yet.");
  });

  it("says the page's title once and keeps each section's facts", async () => {
    renderAt("/forge/p/shop/secrets");
    expect(await screen.findByRole("heading", { level: 1, name: "Secrets" })).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    const shop = screen.getByTestId("several-section-shop");
    expect(within(shop).getByText("shop meta")).toBeInTheDocument();
    expect(within(shop).getByRole("heading", { level: 2, name: /Shop/ })).toBeInTheDocument();
    expect(within(shop).queryByText("Secrets")).not.toBeInTheDocument();
  });

  it("narrows to one project with ?only=", async () => {
    renderAt("/forge/p/shop/secrets?only=blog");
    expect(await screen.findByTestId("several-section-blog")).toBeInTheDocument();
    expect(screen.queryByTestId("several-section-shop")).not.toBeInTheDocument();
    expect(screen.getByTestId("project-filter-blog")).toHaveAttribute("aria-pressed", "true");
  });
});

describe("severalModeOf", () => {
  it("aggregates every list page, and leaves a page about one object alone", () => {
    expect(severalModeOf({ kind: "services" })).toBe("self");
    expect(severalModeOf({ kind: "obs-view", view: "logs" })).toBe("self");
    expect(severalModeOf({ kind: "obs-overview" })).toBe("self");
    expect(severalModeOf({ kind: "releases" })).toBe("sections");
    expect(severalModeOf({ kind: "secrets" })).toBe("sections");
    expect(severalModeOf({ kind: "environments" })).toBe("sections");
    expect(severalModeOf({ kind: "release", version: "v1" })).toBe("one");
    expect(severalModeOf({ kind: "workload", env: "prod", name: "api" })).toBe("one");
  });
});
