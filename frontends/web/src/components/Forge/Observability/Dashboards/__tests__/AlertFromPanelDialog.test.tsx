// Copyright (c) 2025 Reliant Labs

/**
 * "Create alert from this panel": the dialog over the snippet generator, and
 * the two places that open it (a panel's menu, and the panel editor).
 */

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { Panel } from "@/services/controlPlane/observability/types";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import type { ObsEnvironment } from "../../obsPlace";
import { AlertFromPanelDialog } from "../AlertFromPanelDialog";
import type { DashboardSearch } from "../dashboardModel";
import { DashboardView, type DashboardHrefs } from "../DashboardView";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const p95: Panel = {
  id: "p95",
  title: "p95 checkout",
  kind: "number",
  signal: "traces",
  where: [{ field: "span.kind", op: "eq", value: "server" }],
  series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }],
  unit: "ms",
};

const heatmap: Panel = { id: "h", title: "Latency distribution", kind: "heatmap", signal: "traces", series: [{ label: "latency", agg: "count", field: "span.duration" }] };

const envs: ObsEnvironment[] = FIXTURE_ENVS.map((e) => ({ id: e.id, name: e.name }));
const withRules = (own: string[]): ObsEnvironment[] =>
  envs.map((e) =>
    e.name === "prod"
      ? {
          ...e,
          declared: {
            alerts: [
              ...own.map((alert) => ({ alert, expr: "SELECT 1 AS value", for: "5m", interval: "1m", keepFiringFor: "", labels: {}, annotations: {}, source: "user" as const, defaultId: "" })),
              { alert: "ServiceSilent", expr: "SELECT 1 AS value", for: "5m", interval: "1m", keepFiringFor: "", labels: {}, annotations: {}, source: "default" as const, defaultId: "service-silent" },
            ],
            alertRoutes: [],
            observability: null,
          },
        }
      : e
  );

function open(panel: Panel, over: Partial<Parameters<typeof AlertFromPanelDialog>[0]> = {}) {
  const onClose = vi.fn();
  render(<AlertFromPanelDialog panel={panel} dashboardTitle="Checkout latency" environments={envs} env="prod" services={[]} onClose={onClose} {...over} />);
  return { onClose };
}

const kcl = () => screen.getByTestId("alert-kcl").textContent ?? "";

describe("the dialog", () => {
  it("prefills the rule from the panel: its query, a threshold, comparator, window and severity, as editable fields", () => {
    open(p95);
    expect(screen.getByTestId("alert-name")).toHaveValue("P95CheckoutHigh");
    expect(screen.getByTestId("alert-comparator")).toHaveValue("above");
    expect(screen.getByTestId("alert-threshold")).toHaveValue("500");
    expect(screen.getByTestId("alert-window")).toHaveValue("5m");
    expect(screen.getByTestId("alert-hold")).toHaveValue("5m");
    expect(screen.getByTestId("alert-severity")).toHaveValue("warning");
    expect(screen.getByTestId("alert-env")).toHaveValue("prod");
    expect(kcl()).toContain('alert = "P95CheckoutHigh"');
    expect(kcl()).toContain("quantileExactWeightedIf(0.95)");
    expect(kcl()).toContain("{reliant_env_id:String}");
    expect(kcl()).toContain("HAVING value > 500");
    expect(screen.getByTestId("alert-copy")).toBeEnabled();
  });

  it("rewrites the snippet as the fields change", async () => {
    const user = userEvent.setup();
    open(p95);
    const threshold = screen.getByTestId("alert-threshold");
    await user.clear(threshold);
    await user.type(threshold, "800");
    await user.selectOptions(screen.getByTestId("alert-severity"), "critical");
    await user.selectOptions(screen.getByTestId("alert-comparator"), "below");
    const window = screen.getByTestId("alert-window");
    await user.clear(window);
    await user.type(window, "1h");
    expect(kcl()).toContain("HAVING value < 800");
    expect(kcl()).toContain("INTERVAL 1 HOUR");
    expect(kcl()).toContain('labels = {severity = "critical"}');
    expect(screen.getByTestId("alert-name")).toHaveValue("P95CheckoutLow");
  });

  it("keeps a name the reader typed when the comparator changes", async () => {
    const user = userEvent.setup();
    open(p95);
    const name = screen.getByTestId("alert-name");
    await user.clear(name);
    await user.type(name, "MyRule");
    await user.selectOptions(screen.getByTestId("alert-comparator"), "below");
    expect(name).toHaveValue("MyRule");
  });

  it("says what is wrong on the field, withholds the snippet and disables Copy until it is fixed", async () => {
    const user = userEvent.setup();
    open(p95);
    const threshold = screen.getByTestId("alert-threshold");
    await user.clear(threshold);
    await user.type(threshold, "fast");
    expect(screen.getByTestId("alert-problem-threshold")).toHaveTextContent("A number, like 0.5.");
    expect(screen.queryByTestId("alert-kcl")).not.toBeInTheDocument();
    expect(screen.getByTestId("alert-snippet-pending")).toBeInTheDocument();
    expect(screen.getByTestId("alert-copy")).toBeDisabled();
    const window = screen.getByTestId("alert-window");
    await user.clear(window);
    await user.type(window, "30s");
    expect(screen.getByTestId("alert-problem-window")).toHaveTextContent("At least 1m");
    await user.clear(threshold);
    await user.type(threshold, "1");
    await user.clear(window);
    await user.type(window, "5m");
    expect(screen.getByTestId("alert-copy")).toBeEnabled();
  });

  it("copies the whole block when the environment declares nothing yet, and just the rule when asked", async () => {
    const user = userEvent.setup();
    open(p95);
    expect(screen.getByTestId("alert-copy")).toHaveTextContent("Copy block");
    expect(kcl()).toMatch(/^observability = forge\.Observability \{\n {4}alerts = forge\.Alerts \{\n {8}rules = \[/);
    await user.click(screen.getByTestId("alert-copy"));
    await waitFor(() => expect(screen.getByTestId("alert-copy")).toHaveTextContent("Copied"));
    expect(await navigator.clipboard.readText()).toMatch(/^observability = forge\.Observability/);
    await user.click(screen.getByRole("button", { name: "Rule only" }));
    expect(screen.getByTestId("alert-copy")).toHaveTextContent("Copy rule");
    await user.click(screen.getByTestId("alert-copy"));
    await waitFor(async () => expect(await navigator.clipboard.readText()).toMatch(/^forge\.AlertRule \{\n {4}alert = "P95CheckoutHigh"/));
  });

  it("says where to paste it: the environment's file, and what the environment already declares", async () => {
    const user = userEvent.setup();
    open(p95);
    expect(screen.getByTestId("alert-snippet")).toHaveTextContent("deploy/kcl/prod/main.k");
    expect(screen.getByTestId("alert-where")).toHaveTextContent("has no recorded shape yet");
    await user.selectOptions(screen.getByTestId("alert-env"), "staging");
    expect(screen.getByTestId("alert-snippet")).toHaveTextContent("deploy/kcl/staging/main.k");
    expect(kcl()).toContain("alert = ");
  });

  it("names the rules the environment already has, and refuses a name that would collide", async () => {
    const user = userEvent.setup();
    open(p95, { environments: withRules(["P95CheckoutHigh", "OrdersBacklog"]) });
    expect(screen.getByTestId("alert-problem-name")).toHaveTextContent("prod already declares a rule called P95CheckoutHigh");
    expect(screen.getByTestId("alert-copy")).toBeDisabled();
    expect(screen.getByTestId("alert-where")).toHaveTextContent("already declares 2 rules of its own (P95CheckoutHigh, OrdersBacklog)");
    expect(screen.getByRole("button", { name: "Rule only" })).toHaveAttribute("aria-pressed", "true");
    const name = screen.getByTestId("alert-name");
    await user.clear(name);
    await user.type(name, "CheckoutP95Spike");
    expect(screen.getByTestId("alert-copy")).toBeEnabled();
  });

  it("offers a choice of series when the panel plots several, and watches the one picked", async () => {
    const user = userEvent.setup();
    const two: Panel = { ...p95, series: [...p95.series, { label: "p50", agg: "quantile", quantile: 0.5, field: "span.duration" }] };
    open(two);
    expect(kcl()).toContain("quantileExactWeightedIf(0.95)");
    await user.selectOptions(screen.getByTestId("alert-series"), "1");
    expect(kcl()).toContain("quantileExactWeightedIf(0.5)");
    expect(screen.getByTestId("alert-notes")).toHaveTextContent("Watches p50");
  });

  it("shows the SQL panel's own query inside the rule, and refuses SQL the alert guard refuses", () => {
    const sql: Panel = { id: "q", title: "Slow queries", kind: "sql", signal: "", series: [], sql: "SELECT ServiceName AS service, count() AS value FROM otel_logs WHERE Timestamp >= {from:DateTime64(9)} GROUP BY service" };
    const { unmount } = render(<AlertFromPanelDialog panel={sql} environments={envs} env="prod" services={[]} onClose={vi.fn()} />);
    expect(kcl()).toContain("Timestamp >= (now64(9) - INTERVAL 5 MINUTE)");
    expect(kcl()).toContain("WHERE value > ");
    unmount();
    render(<AlertFromPanelDialog panel={{ ...sql, sql: "SELECT * FROM system.users" }} environments={envs} env="prod" services={[]} onClose={vi.fn()} />);
    expect(screen.getByTestId("alert-expr-problem")).toHaveTextContent("would refuse this query as an alert rule");
    expect(screen.getByTestId("alert-copy")).toBeDisabled();
  });

  it("explains a heatmap instead of writing a rule, and offers only Close", async () => {
    const user = userEvent.setup();
    const { onClose } = open(heatmap);
    expect(screen.getByTestId("alert-unsupported-reason")).toHaveTextContent("how values are distributed over time");
    expect(screen.getByTestId("alert-unsupported-instead")).toHaveTextContent("p95 (or max) series over span.duration");
    expect(screen.queryByTestId("alert-copy")).not.toBeInTheDocument();
    expect(screen.queryByTestId("alert-kcl")).not.toBeInTheDocument();
    await user.click(screen.getByTestId("alert-dialog-close"));
    expect(onClose).toHaveBeenCalled();
  });

  it("explains a counter metric, which a panel computes from differences between exports", () => {
    const counter: Panel = { id: "c", title: "Requests", kind: "line", signal: "metrics", series: [{ label: "r", agg: "sum", field: "http.requests", metricType: "sum" }] };
    open(counter);
    expect(screen.getByTestId("alert-unsupported-reason")).toHaveTextContent("counter");
    expect(screen.getByTestId("alert-unsupported-instead")).toHaveTextContent("alert-defaults_gen.json");
  });
});

// ── Where it opens from ──────────────────────────────────────────────────────

const hrefs: DashboardHrefs = {
  logs: () => "/logs",
  traces: () => "/traces",
  workload: (name) => `/workloads/${name}`,
  list: "/dashboards",
  release: (v) => `/releases/${v}`,
  trace: (id) => `/trace/${id}`,
  dashboard: (ref) => `/d/${ref.kind}/${ref.key}`,
};

function renderDashboard(key: string, kind: "workspace" | "release" = "workspace") {
  function Stateful() {
    const [search, setSearch] = useState<DashboardSearch>({});
    return (
      <DashboardView
        project={FIXTURE_PROJECT}
        dashRef={{ kind, projectId: FIXTURE_PROJECT, key }}
        env="prod"
        environments={FIXTURE_ENVS}
        search={search}
        onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))}
        onEnvChange={vi.fn()}
        hrefs={hrefs}
        navigateTo={vi.fn()}
      />
    );
  }
  renderWithApi(createFixtureApi(), <Stateful />);
}

describe("opening it from a dashboard", () => {
  it("is an entry in every panel's menu, and opens the dialog for that panel", async () => {
    const user = userEvent.setup();
    renderDashboard("checkout-latency");
    await user.click(await screen.findByTestId("dashboard-panel-menu-p95"));
    await user.click(await screen.findByTestId("dashboard-panel-alert-p95"));
    const dialog = await screen.findByTestId("alert-dialog");
    expect(within(dialog).getByTestId("alert-name")).toHaveValue("P95CheckoutHigh");
    expect(within(dialog).getByTestId("alert-env")).toHaveValue("prod");
    expect(within(dialog).getByTestId("alert-kcl")).toHaveTextContent("SpanName = 'orders.v1.OrdersService/CreateOrder'");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByTestId("alert-dialog")).not.toBeInTheDocument());
  });

  it("is offered on a panel that is not a number too, and explains why not", async () => {
    const user = userEvent.setup();
    renderDashboard("checkout-latency");
    await user.click(await screen.findByTestId("dashboard-panel-menu-conversion"));
    await user.click(await screen.findByTestId("dashboard-panel-alert-conversion"));
    expect(await screen.findByTestId("alert-unsupported-reason")).toHaveTextContent("conversion across ordered steps");
  });

  it("is available on a read-only release dashboard: it only reads the panel", async () => {
    const user = userEvent.setup();
    renderDashboard("overview", "release");
    const menu = (await screen.findAllByTestId(/^dashboard-panel-menu-/))[0]!;
    await user.click(menu);
    const entry = await screen.findByRole("menuitem", { name: /Create alert from this panel/ });
    expect(entry).toBeEnabled();
  });

  it("is a button in the panel editor, and leaves the draft being edited alone", async () => {
    const user = userEvent.setup();
    renderDashboard("checkout-latency");
    await user.click(await screen.findByTestId("dashboard-panel-menu-p95"));
    await user.click(await screen.findByTestId("dashboard-panel-edit-p95"));
    const drawer = await screen.findByTestId("panel-drawer");
    const title = within(drawer).getByTestId("panel-builder-title");
    await user.clear(title);
    await user.type(title, "Checkout p95 now");
    await user.click(within(drawer).getByTestId("panel-drawer-create-alert"));
    const dialog = await screen.findByTestId("alert-dialog");
    expect(within(dialog).getByTestId("alert-name")).toHaveValue("CheckoutP95NowHigh");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByTestId("alert-dialog")).not.toBeInTheDocument());
    expect(screen.getByTestId("panel-drawer")).toBeInTheDocument();
    expect(within(screen.getByTestId("panel-drawer")).getByTestId("panel-builder-title")).toHaveValue("Checkout p95 now");
  });

  it("is disabled in the editor while an alertable panel is incomplete", async () => {
    const user = userEvent.setup();
    renderDashboard("checkout-latency");
    await user.click(await screen.findByTestId("dashboard-add-panel"));
    const drawer = await screen.findByTestId("panel-drawer");
    const create = within(drawer).getByTestId("panel-drawer-create-alert");
    await user.clear(within(drawer).getByTestId("panel-builder-title"));
    expect(create).toBeDisabled();
    await user.type(within(drawer).getByTestId("panel-builder-title"), "Slow checkouts");
    expect(create).toBeEnabled();
  });

  it("opens from the editor for a panel that can never be an alert, and explains instead of staying greyed out", async () => {
    const user = userEvent.setup();
    renderDashboard("checkout-latency");
    await user.click(await screen.findByTestId("dashboard-add-panel"));
    const drawer = await screen.findByTestId("panel-drawer");
    await user.click(within(within(drawer).getByTestId("panel-builder-kind")).getByRole("radio", { name: "Funnel" }));
    expect(within(drawer).getByTestId("panel-drawer-problems")).toHaveTextContent("Name every funnel step.");
    await user.click(within(drawer).getByTestId("panel-drawer-create-alert"));
    expect(await screen.findByTestId("alert-unsupported-reason")).toHaveTextContent("conversion across ordered steps");
  });
});
