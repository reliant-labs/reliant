// Copyright (c) 2025 Reliant Labs

/**
 * Pivots into the explorer (MX-R3): one test per page, each opening the real
 * page, taking the link it offers and decoding it through the explorer's own
 * `stateFromSearch` — so what is asserted is the panel the explorer will open
 * on, not the string that was built. Then the pure rules: which window an alert
 * needs, which labels become filters, and what is never offered.
 */

import { useState, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { defaultParseSearch } from "@tanstack/react-router";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { ForgeConsoleContext, type ForgeConsole } from "../../../Console/consoleContext";
import { STRIP_ENVS, WORKLOADS_PROJECT, createWorkloadsFixtureApi, fixtureHostedObservation } from "../../../Workloads/__fixtures__/workloadFixtures";
import { WorkloadsProviders } from "../../../Workloads/__tests__/workloadsTestUtils";
import { WorkloadDetail } from "../../../Workloads/WorkloadDetail";
import { FIXTURE_DECLARED } from "../../__fixtures__/fixtureAlerting";
import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT } from "../../__fixtures__/fixtureApi";
import { DashboardView, type DashboardHrefs } from "../../Dashboards/DashboardView";
import type { DashboardSearch } from "../../Dashboards/dashboardModel";
import ObservabilityPanel from "../../ObservabilityPanel";
import type { ObsPlace } from "../../obsPlace";
import type { ObsNavigate, ObsSearch } from "../../obsSearch";
import { renderPanel } from "../../__tests__/obsTestUtils";
import { stateFromSearch } from "../model/explorerState";
import { alertPivot, alertRange, dashboardPivot, exploreHrefFor, labelFilter, rangeReaching, tracePivot, workloadPivot } from "../pivots";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../__tests__/obsMocks")).obsMocks.virtual());
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const SLOW = { timeout: 8000 };
const EXPLORER = "/forge/p/shop/observability/metrics";

/** The console half the pivot hook reads: the forge project and the scope. */
function ConsoleScope({ env = "prod", children }: { env?: string; children: ReactNode }) {
  const value = { forgeProject: "shop", projectId: "proj-1", env, setEnv: vi.fn(), page: { kind: "observability" }, href: () => "/" } as unknown as ForgeConsole;
  return <ForgeConsoleContext.Provider value={value}>{children}</ForgeConsoleContext.Provider>;
}

/** An explorer link's target: its path, and the state the explorer page reads from it (the router's parse, then strings, as the page does). */
function decode(href: string) {
  const url = new URL(href, "http://console.test");
  const parsed = defaultParseSearch(url.search) as Record<string, unknown>;
  const strings: Record<string, string | undefined> = {};
  for (const [key, value] of Object.entries(parsed)) if (typeof value !== "object") strings[key] = String(value);
  return {
    path: url.pathname,
    state: stateFromSearch(strings),
    env: parsed.env as string | undefined,
    range: parsed.range as string | undefined,
    filters: parsed.f as string[] | undefined,
  };
}

describe("a dashboard panel", () => {
  const hrefs: DashboardHrefs = {
    logs: () => "/logs",
    traces: () => "/traces",
    workload: (name) => `/workloads/${name}`,
    list: "/dashboards",
    release: (v) => `/releases/${v}`,
    trace: (id) => `/trace/${id}`,
    dashboard: (ref) => `/d/${ref.kind}/${ref.key}`,
  };

  function renderDashboard(initial: DashboardSearch = {}) {
    const api = createFixtureApi();
    function Stateful() {
      const [search, setSearch] = useState<DashboardSearch>(initial);
      return (
        <ConsoleScope>
          <DashboardView
            project={FIXTURE_PROJECT}
            dashRef={{ kind: "workspace", projectId: FIXTURE_PROJECT, key: "daemon-fleet" }}
            env="prod"
            environments={FIXTURE_ENVS}
            search={search}
            onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))}
            onEnvChange={vi.fn()}
            hrefs={hrefs}
            navigateTo={vi.fn()}
          />
        </ConsoleScope>
      );
    }
    return renderWithApi(api, <Stateful />);
  }

  it("opens the saved query with compare, ignoring an undeclared process URL filter", async () => {
    const user = userEvent.setup();
    renderDashboard({ service: "api", compare: "period", range: "6h" });
    await user.click(await screen.findByTestId("dashboard-panel-menu-goroutines", {}, SLOW));
    const item = await screen.findByTestId("dashboard-panel-explore-goroutines");
    const { path, state, env, range } = decode(item.getAttribute("href")!);
    expect(path).toBe(EXPLORER);
    expect(env).toBe("prod");
    expect(range).toBe("6h");
    expect(state.panel.signal).toBe("metrics");
    expect(state.panel.series).toEqual([{ label: "goroutines", agg: "sum", field: "go.goroutine.count", metricType: "sum" }]);
    // daemon-fleet declares no service variable. A URL must not silently
    // inject one, especially on mixed telemetry/financial dashboards.
    expect(state.panel.where).toBeUndefined();
    expect(state.compare).toBe("period");
  });

  it("is not offered on a panel of another signal", async () => {
    const user = userEvent.setup();
    renderDashboard();
    await user.click(await screen.findByTestId("dashboard-panel-menu-online", {}, SLOW));
    await screen.findByText("Explore in Traces");
    expect(screen.queryByTestId("dashboard-panel-explore-online")).not.toBeInTheDocument();
  });
});

describe("a trace", () => {
  it("opens the service's latency and call rate, narrowed to the selected span's RPC", async () => {
    const user = userEvent.setup();
    const api = createFixtureApi();
    renderPanelInConsole(api, { view: "traces", trace: "4bf92f3577b34da6a3ce929d0e0e4736" });
    const link = await screen.findByTestId("trace-metrics", {}, SLOW);
    // The root span names an RPC and its service.
    const root = decode(link.getAttribute("href")!);
    expect(root.path).toBe(EXPLORER);
    expect(root.state.panel.where).toEqual([
      { field: "service.name", op: "eq", value: "api" },
      { field: "metric.attr.rpc.method", op: "eq", value: "CreateOrder" },
    ]);
    expect(root.state.panel.series).toEqual([
      { label: "A", agg: "quantile", quantile: 0.95, field: "rpc.server.duration", metricType: "histogram" },
      { label: "B", agg: "rate", field: "rpc.server.calls", metricType: "sum" },
    ]);

    // A span with no RPC method: the service alone.
    await user.click(await screen.findByText("SELECT orders"));
    await waitFor(() => expect(decode(screen.getByTestId("trace-metrics").getAttribute("href")!).state.panel.where).toEqual([{ field: "service.name", op: "eq", value: "api" }]));
  });
});

describe("an alert", () => {
  const ENVIRONMENTS = FIXTURE_ENVS.map((env) => ({ ...env, declared: FIXTURE_DECLARED[env.id] }));
  const PROJECT: ObsPlace = { kind: "project", project: FIXTURE_PROJECT, environments: ENVIRONMENTS };

  it("opens the SQL tab on the rule's query, filtered to the instance's allowlisted labels", async () => {
    renderPanelInConsole(createFixtureApi(), { view: "alerts" }, PROJECT);
    const link = await screen.findByTestId("obs-alert-explore-CheckoutP95High", {}, { timeout: 10_000 });
    const { path, state, filters, env, range } = decode(link.getAttribute("href")!);
    expect(path).toBe(EXPLORER);
    expect(env).toBe("prod");
    expect(state.tab).toBe("sql");
    expect(state.sqlPanel?.kind).toBe("sql");
    expect(state.sqlPanel?.sql).toMatch(/^SELECT ServiceName AS service, quantile\(0\.95\)\(Duration\)/);
    // `team` is not an attribute the scope allowlists; `service` is the service.
    expect(filters).toEqual(["service.name:api"]);
    expect(range).toBeDefined();
  });

  it("is offered from a declared rule too, over the last hour", async () => {
    const user = userEvent.setup();
    renderPanelInConsole(createFixtureApi(), { view: "alerts", alerts: "rules" }, PROJECT);
    await user.click(await screen.findByTestId("obs-rule-prod/CheckoutP95High", {}, { timeout: 10_000 }));
    const link = await screen.findByTestId("obs-rule-explore");
    const { state, filters, range } = decode(link.getAttribute("href")!);
    expect(state.sqlPanel?.sql).toMatch(/quantile\(0\.95\)/);
    expect(filters).toBeUndefined();
    expect(range).toBeUndefined();
  });
});

describe("a workload", () => {
  it("opens each container metric the page charts, for the workload, a line per container", async () => {
    const name = "reliant-temporal-worker";
    const prod = STRIP_ENVS.find((candidate) => candidate.name === "prod")!;
    render(
      <WorkloadsProviders obs={createWorkloadsFixtureApi()}>
        <ConsoleScope>
          <WorkloadDetail
            project={WORKLOADS_PROJECT}
            env={{ id: prod.id, name: prod.name }}
            name={name}
            search={{ range: "6h" }}
            onSearch={vi.fn()}
            hrefs={{ overview: "/overview", release: (version) => `/r/${version}`, activity: "/activity", dashboards: "/dashboards" }}
            actions={{ onRestart: vi.fn(), onSetRunState: vi.fn().mockResolvedValue(undefined), unavailableReason: null }}
            hosted={fixtureHostedObservation("prod", name)}
          />
        </ConsoleScope>
      </WorkloadsProviders>
    );
    const cpu = decode((await screen.findByTestId("workload-chart-cpu-explore", {}, SLOW)).getAttribute("href")!);
    expect(cpu.path).toBe(EXPLORER);
    expect(cpu.env).toBe("prod");
    expect(cpu.range).toBe("6h");
    expect(cpu.state.panel.series).toEqual([{ label: "A", agg: "avg", field: "container.cpu.usage", metricType: "gauge" }]);
    expect(cpu.state.panel.where).toEqual([{ field: "resource.attr.k8s.workload.name", op: "eq", value: name }]);
    expect(cpu.state.panel.groupBy).toEqual(["resource.attr.k8s.container.name"]);

    const memory = decode(screen.getByTestId("workload-chart-memory-explore").getAttribute("href")!);
    expect(memory.state.panel.series[0]!.field).toBe("container.memory.working_set");
  });
});

describe("outside the console", () => {
  it("offers no pivot, rather than a link that cannot resolve", async () => {
    renderPanel(createFixtureApi(), { view: "traces", trace: "4bf92f3577b34da6a3ce929d0e0e4736" });
    await screen.findByTestId("trace-detail", {}, SLOW);
    expect(screen.queryByTestId("trace-metrics")).not.toBeInTheDocument();
  });
});

describe("the pivot rules", () => {
  it("picks the smallest range that reaches back past an alert's firing hour", () => {
    const now = Date.UTC(2026, 9, 24, 12);
    // Fired five minutes ago: the hour before that is 65 minutes back, past the one-hour preset.
    expect(alertRange(now - 5 * 60_000, now)).toBe("6h");
    expect(rangeReaching(now - 30 * 60_000, now)).toBe("1h");
    expect(rangeReaching(now - 90 * 60_000, now)).toBe("6h");
    expect(rangeReaching(now - 20 * 3600_000, now)).toBe("24h");
    expect(rangeReaching(now - 90 * 24 * 3600_000, now)).toBe("7d");
    // No firing time: the hour before now.
    expect(alertRange(null, now)).toBe("1h");
  });

  it("turns an alert label into a filter only when the scope can filter on it", () => {
    expect(labelFilter("service", "api")).toBe("service.name:api");
    expect(labelFilter("service_name", "api")).toBe("service.name:api");
    expect(labelFilter("k8s.workload.name", "api")).toBe("k8s.workload.name:api");
    expect(labelFilter("severity", "critical")).toBeNull();
    expect(labelFilter("alertname", "X")).toBeNull();
    expect(labelFilter("team", "payments")).toBeNull();
    expect(labelFilter("deployment.environment.name", "prod")).toBeNull();
    expect(labelFilter("service", "")).toBeNull();
    expect(labelFilter("not a key!", "x")).toBeNull();
  });

  it("refuses an alert with no query, and a dashboard panel that is not an explorable metrics panel", () => {
    expect(alertPivot("   ")).toBeNull();
    const panel = { id: "p", title: "p", kind: "heatmap", signal: "metrics", series: [{ label: "A", agg: "avg", field: "m" }] };
    expect(dashboardPivot(panel, {})).toBeNull();
    expect(dashboardPivot({ ...panel, kind: "line", signal: "traces" }, {})).toBeNull();
    expect(dashboardPivot({ ...panel, kind: "line", series: [{ label: "A", agg: "count" }] }, {})).toBeNull();
  });

  it("does not add a service the panel already filters on, and drops 'off' from compare", () => {
    const panel = { id: "p", title: "p", kind: "line", signal: "metrics", series: [{ label: "A", agg: "avg", field: "m" }], where: [{ field: "service.name", op: "eq", value: "worker" }] };
    const pivot = dashboardPivot(panel, { service: "api", compare: "off" })!;
    expect(pivot.panel.where).toEqual([{ field: "service.name", op: "eq", value: "worker" }]);
    expect(pivot.extra).toBeUndefined();
  });

  it("hides the link when the state cannot survive a URL, instead of linking to an empty explorer", () => {
    const href = exploreHrefFor(EXPLORER, "prod");
    expect(href(null)).toBeNull();
    expect(href(alertPivot("SELECT 1 " + "x".repeat(9000)))).toBeNull();
    expect(href(tracePivot({ service: "api", attributes: {} }))).toContain("q=");
    expect(href(tracePivot({ service: "", attributes: {} }))).toBeNull();
  });

  it("carries the console env, and a range only when it is not the default", () => {
    const href = exploreHrefFor(EXPLORER, "staging");
    const pivot = workloadPivot("api", "cpu");
    expect(decode(href(pivot)!).env).toBe("staging");
    expect(decode(href(pivot, { env: "prod", range: "1h" })!).env).toBe("prod");
    expect(decode(href(pivot, { range: "1h" })!).range).toBeUndefined();
    expect(decode(href(pivot, { range: "24h" })!).range).toBe("24h");
  });
});

// ── helpers ──────────────────────────────────────────────────────────────────

function renderWithApi(api: ObservabilityApi, ui: ReactNode) {
  return render(
    <QueryClientProvider client={createTestQueryClient()}>
      <ObservabilityApiProvider api={api}>{ui}</ObservabilityApiProvider>
    </QueryClientProvider>
  );
}

/** The Observability panel as the console hosts it: inside the console's scope context. */
function renderPanelInConsole(api: ObservabilityApi, initial: ObsSearch, place: ObsPlace = { kind: "env", env: { id: "env-prod", name: "prod" } }) {
  function Stateful() {
    const [search, setSearch] = useState<ObsSearch>(initial);
    const navigate: ObsNavigate = (next) => setSearch((prev) => ({ ...prev, ...next }));
    return (
      <ConsoleScope>
        <ObservabilityPanel place={place} search={search} navigate={navigate} />
      </ConsoleScope>
    );
  }
  return renderWithApi(api, <Stateful />);
}
