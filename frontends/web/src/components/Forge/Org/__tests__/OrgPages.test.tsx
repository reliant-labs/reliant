// Copyright (c) 2025 Reliant Labs

/**
 * The org layout and the org Observability page, mounted on a real router with
 * the data hooks stubbed:
 *
 *   - the layout draws the org chrome around the page, and draws NOTHING but
 *     the page when the URL names one app (the resolve-and-redirect case);
 *   - the page builds an org place from the org's environments and hands the panel the
 *     org keys of the URL — and routes Dashboards and Metrics to their own
 *     bodies instead;
 *   - a view switch from inside the panel navigates to the org page hosting it.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
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

import type { CloudEnv } from "@/services/forge/cloudEnvs";
import { QueryClientProvider } from "@tanstack/react-query";
import { createTestQueryClient } from "@/test/renderWithQuery";

const state = vi.hoisted(() => ({
  envs: { isLoading: false, data: undefined as unknown },
  appNames: [] as string[],
  alerts: [] as { state: string }[],
  panelProps: [] as { place: unknown; search: unknown }[],
  panelNavigate: undefined as undefined | ((next: Record<string, unknown>) => void),
}));

// Org-switcher behavior is exercised in components/Orgs; this fixture has one org.
vi.mock("@/components/Orgs/OrgSwitcher", () => ({ OrgSwitcher: () => null }));

vi.mock("@/hooks/apps-queries", () => ({
  useOrgEnvironments: () => state.envs,
  useApps: () => ({ apps: state.appNames.map((name) => ({ name })), loading: false, availability: "available", detail: "" }),
}));
vi.mock("@/hooks/observability-queries", () => ({
  useObservabilityOrg: () => ({ orgId: "org-1", isLoading: false, error: null, refetch: vi.fn() }),
  useAlerts: () => ({ data: state.alerts }),
}));
vi.mock("@/store/authStore", () => ({
  useAuthStore: (select: (s: { user: null }) => unknown) => select({ user: null }),
}));
vi.mock("../../Observability/ObservabilityPanel", () => ({
  default: (props: { place: unknown; search: unknown; navigate: (next: Record<string, unknown>) => void }) => {
    state.panelProps.push(props);
    state.panelNavigate = props.navigate;
    return <div data-testid="panel-stub" />;
  },
}));
vi.mock("../OrgDashboards", () => ({ OrgDashboards: () => <div data-testid="org-dashboards-stub" /> }));

import { OrgConsoleLayout } from "../OrgConsoleLayout";
import { OrgObservabilityPage } from "../OrgObservabilityPage";

const app = (project: string, ...envs: [string, string][]) => ({
  project,
  envs: envs.map(([id, name]): CloudEnv => ({ id, name, project, kind: "persistent" })),
});

function available(apps: ReturnType<typeof app>[]) {
  state.appNames = apps.map((a) => a.project);
  state.envs = { isLoading: false, data: { availability: "available", envs: apps.flatMap((a) => a.envs), detail: "" } };
}

async function mount(url: string) {
  const root = createRootRoute({ component: Outlet });
  const layout = createRoute({ getParentRoute: () => root, id: "_org", component: OrgConsoleLayout });
  const pages = ["/forge/observability", "/forge/observability/$view"].map((path) =>
    createRoute({
      getParentRoute: () => layout,
      path,
      validateSearch: (raw: Record<string, unknown>) => raw,
      component: OrgObservabilityPage,
    })
  );
  const router = createRouter({
    routeTree: root.addChildren([layout.addChildren(pages)]),
    history: createMemoryHistory({ initialEntries: [url] }),
  });
  render(<QueryClientProvider client={createTestQueryClient()}><RouterProvider router={router} /></QueryClientProvider>);
  await waitFor(() => expect(router.state.status).toBe("idle"));
  return router;
}

beforeEach(() => {
  state.envs = { isLoading: false, data: undefined };
  state.appNames = [];
  state.alerts = [];
  state.panelProps = [];
  state.panelNavigate = undefined;
  window.localStorage.clear();
});

describe("OrgConsoleLayout", () => {
  it("draws the org chrome around the page, with Apps opening onto /apps and each app", async () => {
    available([app("api", ["e1", "prod"]), app("shop", ["e2", "prod"])]);
    const user = userEvent.setup();
    await mount("/forge/observability");
    expect(await screen.findByTestId("forge-org-shell")).toBeInTheDocument();
    expect(await screen.findByTestId("panel-stub")).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Organization console" });
    expect(nav).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: "Apps" })[0]!);
    expect(screen.getAllByRole("link", { name: "All apps" }).some((a) => a.getAttribute("href") === "/apps")).toBe(true);
    expect(screen.getAllByRole("link", { name: "api" }).some((a) => a.getAttribute("href") === "/forge/p/api")).toBe(true);
    expect(screen.getByRole("navigation", { name: "Breadcrumb" })).toHaveTextContent("Observability");
  });

  it("is a bare outlet when the URL names one app", async () => {
    available([app("api", ["e1", "prod"])]);
    await mount("/forge/observability?forgeProject=api");
    expect(await screen.findByTestId("panel-stub")).toBeInTheDocument();
    expect(screen.queryByTestId("forge-org-shell")).not.toBeInTheDocument();
  });

  it("is a bare outlet for a Reliant project id too", async () => {
    available([]);
    await mount("/forge/observability?project=p-1");
    expect(screen.queryByTestId("forge-org-shell")).not.toBeInTheDocument();
  });

  it("badges Alerts with the org-wide firing count", async () => {
    available([app("api", ["e1", "prod"])]);
    state.alerts = [{ state: "firing" }, { state: "firing" }, { state: "resolved" }];
    await mount("/forge/observability/logs");
    await screen.findByTestId("forge-org-shell");
    expect(screen.getAllByText("2").length).toBeGreaterThan(0);
    expect(screen.getByRole("navigation", { name: "Breadcrumb" })).toHaveTextContent("Observability›Logs");
  });

  it("remembers the collapsed rail", async () => {
    available([]);
    window.localStorage.setItem("forge.console.rail", "1");
    await mount("/forge/observability");
    await screen.findByTestId("forge-org-shell");
    expect(window.localStorage.getItem("forge.console.rail")).toBe("1");
  });
});

describe("OrgObservabilityPage", () => {
  const APPS = [app("api", ["e1", "prod"]), app("shop", ["e2", "prod"], ["e3", "dev"])];

  it("hands the panel an org place of every app and environment, named app/env and sorted", async () => {
    available(APPS);
    await mount("/forge/observability/logs?q=timeout&apps=%5B%22shop%22%5D&envs=%5B%22shop%2Fprod%22%5D");
    await screen.findByTestId("panel-stub");
    const props = state.panelProps.at(-1)!;
    expect(props.place).toEqual({
      kind: "org",
      apps: ["api", "shop"],
      environments: [
        { id: "e1", name: "api/prod", project: "api", declared: null },
        { id: "e3", name: "shop/dev", project: "shop", declared: null },
        { id: "e2", name: "shop/prod", project: "shop", declared: null },
      ],
    });
    expect(props.search).toMatchObject({ view: "logs", q: "timeout", apps: ["shop"], envs: ["shop/prod"] });
  });

  it("hosts the overview at the Observability root", async () => {
    available(APPS);
    await mount("/forge/observability");
    await screen.findByTestId("panel-stub");
    expect(state.panelProps.at(-1)!.search).toMatchObject({ view: "overview" });
  });

  it("shows a skeleton while the environments load, and no panel", async () => {
    state.envs = { isLoading: true, data: undefined };
    await mount("/forge/observability/logs");
    expect(await screen.findByTestId("org-obs-loading")).toBeInTheDocument();
    expect(screen.queryByTestId("panel-stub")).not.toBeInTheDocument();
  });

  it("has a designed empty state when nothing is deployed, rather than an unscoped panel", async () => {
    available([]);
    await mount("/forge/observability/logs");
    expect(await screen.findByTestId("org-obs-no-envs")).toHaveTextContent("No environments to observe yet");
    expect(screen.queryByTestId("panel-stub")).not.toBeInTheDocument();
  });

  it("explains an unreachable control plane", async () => {
    state.envs = { isLoading: false, data: { availability: "unreachable", envs: [], detail: "timeout" } };
    await mount("/forge/observability");
    expect(await screen.findByTestId("forge-cloud-unreachable")).toBeInTheDocument();
  });

  it("renders the org-wide list on Dashboards, not the panel", async () => {
    available(APPS);
    await mount("/forge/observability/dashboards");
    expect(await screen.findByTestId("org-dashboards-stub")).toBeInTheDocument();
    expect(screen.queryByTestId("panel-stub")).not.toBeInTheDocument();
  });

  it("points Metrics at Dashboards, keeping the filters", async () => {
    available(APPS);
    await mount("/forge/observability/metrics?apps=%5B%22shop%22%5D");
    const note = await screen.findByTestId("org-metrics");
    const href = note.querySelector("a")!.getAttribute("href")!;
    expect(href.startsWith("/forge/observability/dashboards?apps=")).toBe(true);
  });

  it("navigates to the page hosting a view the panel switches to, carrying the filters", async () => {
    available(APPS);
    const router = await mount("/forge/observability/logs?apps=%5B%22shop%22%5D&range=7d");
    await screen.findByTestId("panel-stub");
    state.panelNavigate!({ view: "traces", trace: "abc" });
    await waitFor(() => expect(router.state.location.pathname).toBe("/forge/observability/traces"));
    expect(router.state.location.search).toMatchObject({ apps: ["shop"], range: "7d", trace: "abc" });
  });

  it("updates the search in place for a change within the view", async () => {
    available(APPS);
    const router = await mount("/forge/observability/logs?q=a");
    await screen.findByTestId("panel-stub");
    state.panelNavigate!({ q: "b", apps: ["api"] });
    await waitFor(() => expect(router.state.location.search).toMatchObject({ q: "b", apps: ["api"] }));
    expect(router.state.location.pathname).toBe("/forge/observability/logs");
  });

  it("is reachable from the nav without a full reload", async () => {
    available(APPS);
    const user = userEvent.setup();
    const router = await mount("/forge/observability/logs");
    await screen.findByTestId("forge-org-shell");
    await user.click(screen.getAllByRole("link", { name: "Observability" })[0]!);
    await waitFor(() => expect(router.state.location.pathname).toBe("/forge/observability"));
  });
});
