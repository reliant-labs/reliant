// Copyright (c) 2025 Reliant Labs

/**
 * The metrics explorer against the fixture app: the catalog (search, groups,
 * badges, keyboard), picking a metric (the type-correct default: counter →
 * rate, histogram → p95, an up-down counter never offered a rate), the builder
 * (a formula, group-by), the chart's notes and comparison, exemplars, the SQL
 * tab, save to dashboard (happy path, a lost race retried, a lost race shown,
 * a non-admin's copy), the URL restoring the page, and the in-flight cap.
 */

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { act, getConfig, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import type { Panel, QueryPanelRequest } from "@/services/controlPlane/observability/types";

import { FIXTURE_ENVS, FIXTURE_PROJECT } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { createExplorerFixtureApi, type ExplorerFixtureOptions } from "../__fixtures__/explorerFixtures";
import { MetricsExplorer, type MetricsExplorerHrefs } from "../MetricsExplorerPage";
import { searchFromState, stateFromSearch, type ExplorerState } from "../model/explorerState";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const hrefs: MetricsExplorerHrefs = {
  trace: (id) => `/trace/${id}`,
  dashboard: (ref) => `/d/${ref.kind}/${ref.key}`,
};

/**
 * Long enough for the builder's 400 ms debounce and a fixture answer under a
 * loaded machine, and never shorter than the load-scaled wait src/test/setup.ts
 * configured (a fixed 4 s used to cut that budget on a busy host).
 */
const SLOW = { timeout: Math.max(4000, getConfig().asyncUtilTimeout) };
// Every test mounts the whole explorer and chains up to four SLOW waits, so
// the default test budget (5 s on an idle host, two default waits' worth) is
// less than one test may honestly need under load: the merge gate timed out
// here while the page was still answering. Three waits' worth, still load-scaled.
vi.setConfig({ testTimeout: 3 * SLOW.timeout });

function renderExplorer(options: ExplorerFixtureOptions = {}, initial: Record<string, unknown> = {}, env = "prod") {
  const onCall = vi.fn();
  // One object, read by the fixture at call time: a test can lift a case (inFlight) mid-way.
  const opts: ExplorerFixtureOptions = { ...options, onCall };
  const fixture = createExplorerFixtureApi(opts);
  const current: { search: Record<string, unknown> } = { search: initial };
  function Stateful() {
    const [search, setSearch] = useState<Record<string, unknown>>(initial);
    return (
      <MetricsExplorer
        project={FIXTURE_PROJECT}
        env={env}
        environments={FIXTURE_ENVS}
        search={search}
        onSearch={(next) =>
          setSearch((prev) => {
            const merged: Record<string, unknown> = { ...prev, ...next };
            for (const key of Object.keys(merged)) if (merged[key] === undefined) delete merged[key];
            current.search = merged;
            return merged;
          })
        }
        hrefs={hrefs}
      />
    );
  }
  renderWithApi(fixture, <Stateful />);
  return { onCall, current, options: opts };
}

const calls = (onCall: ReturnType<typeof vi.fn>, rpc: string) => onCall.mock.calls.filter(([name]) => name === rpc).map(([, req]) => req);
const panelCalls = (onCall: ReturnType<typeof vi.fn>) => (calls(onCall, "QueryPanel") as QueryPanelRequest[]).filter((r) => !r.explain);
const lastPanel = (onCall: ReturnType<typeof vi.fn>): Panel => panelCalls(onCall).at(-1)!.panel;

/**
 * A catalog row by its metric. Found by `data-metric`, not by role and name:
 * computing every row's accessible name took ~100 ms a lookup on an idle host
 * and several times that on a loaded one, which every test pays at least once
 * (a pick) and pushed this file past its test timeout in the merge gate.
 */
async function catalogRow(name: string) {
  const list = await screen.findByTestId("metrics-catalog-list", {}, SLOW);
  const rows = [...list.querySelectorAll<HTMLElement>('[role="option"]')].filter((row) => row.getAttribute("data-metric") === name);
  expect(rows).toHaveLength(1);
  return rows[0]!;
}

async function pick(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(await catalogRow(name));
}

describe("the catalog", () => {
  it("groups metrics, badges the silent and the undeclared, and searches", async () => {
    const user = userEvent.setup();
    renderExplorer();
    const list = await screen.findByTestId("metrics-catalog-list", {}, SLOW);
    expect([...list.querySelectorAll("[data-group]")].map((g) => g.getAttribute("data-group"))).toEqual(["declared", "runtime", "infra", "other"]);
    expect(within(screen.getByTestId("metrics-catalog-group-declared")).getByText("(4)")).toBeInTheDocument();
    expect(within(await catalogRow("orders.refunds")).getByText("no data")).toBeInTheDocument();
    expect(within(await catalogRow("queue.depth")).getByText("undeclared")).toBeInTheDocument();
    expect(within(await catalogRow("orders.created")).queryByText("undeclared")).not.toBeInTheDocument();

    await user.type(screen.getByTestId("metrics-catalog-search"), "latency");
    expect(within(list).getAllByRole("option").map((o) => o.getAttribute("data-metric"))).toEqual(["checkout.latency"]);
    await user.clear(screen.getByTestId("metrics-catalog-search"));
    await user.type(screen.getByTestId("metrics-catalog-search"), "nothing-like-it");
    expect(screen.getByTestId("metrics-catalog-no-match")).toBeInTheDocument();
  });

  it("is keyboard-navigable: arrows move between rows, Enter picks", async () => {
    const user = userEvent.setup();
    const { onCall } = renderExplorer();
    await screen.findByTestId("metrics-catalog-list", {}, SLOW);
    await user.click(screen.getByTestId("metrics-catalog-search"));
    await user.keyboard("{ArrowDown}");
    const options = within(screen.getByTestId("metrics-catalog-list")).getAllByRole("option");
    await waitFor(() => expect(options[0]).toHaveFocus());
    expect(options.filter((o) => o.tabIndex === 0)).toHaveLength(1);
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(options[1]).toHaveFocus());
    await user.keyboard("{End}");
    await waitFor(() => expect(options.at(-1)).toHaveFocus());
    await user.keyboard("{Home}{Enter}");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    await waitFor(() => expect(panelCalls(onCall).length).toBeGreaterThan(0), SLOW);
    expect(lastPanel(onCall).series[0]!.field).toBe(options[0]!.getAttribute("data-metric"));
  });
});

describe("picking a metric", () => {
  it("defaults a counter to its rate, and opens its detail", async () => {
    const user = userEvent.setup();
    const { onCall } = renderExplorer();
    await pick(user, "orders.created");
    expect(screen.getByTestId("metrics-query-A-metric")).toHaveValue("orders.created");
    expect(screen.getByTestId("metrics-query-A-agg")).toHaveValue("rate");
    expect(await screen.findByTestId("metric-detail", {}, SLOW)).toHaveAttribute("data-metric", "orders.created");
    expect(await screen.findByTestId("metric-attributes", {}, SLOW)).toHaveTextContent("user.id");
    await waitFor(() => expect(lastPanel(onCall).series).toEqual([{ label: "A", agg: "rate", field: "orders.created", metricType: "sum" }]), SLOW);
  });

  it("defaults a histogram to p95, and draws its exemplars", async () => {
    const user = userEvent.setup();
    renderExplorer();
    await pick(user, "checkout.latency");
    expect(screen.getByTestId("metrics-query-A-agg")).toHaveValue("quantile");
    expect(screen.getByTestId("metrics-query-A-quantile")).toHaveValue("0.95");
    const dots = await screen.findAllByTestId("metrics-exemplar-dot", {}, SLOW);
    expect(dots.length).toBeGreaterThan(0);
    expect(dots.filter((d) => d.hasAttribute("data-stored")).length).toBeGreaterThan(0);

    await user.click(dots[0]!);
    const popover = await screen.findByTestId("metrics-exemplar-popover");
    const list = await within(popover).findByTestId("metrics-exemplar-list", {}, SLOW);
    const items = within(list).getAllByRole("listitem");
    // Stored traces first, linked; a sampled-out one says so instead.
    expect(items[0]).toHaveAttribute("data-stored");
    expect(within(popover).getAllByTestId("metrics-exemplar-open-trace")[0]).toHaveAttribute("href", expect.stringMatching(/^\/trace\//));
    expect(within(popover).getAllByTestId("metrics-exemplar-sampled-out")[0]).toHaveTextContent("this trace was sampled out");
    await user.keyboard("{Escape}");
    expect(screen.queryByTestId("metrics-exemplar-popover")).not.toBeInTheDocument();
  });

  it("offers Area beside Line, and writes it to the URL", async () => {
    const user = userEvent.setup();
    const { current } = renderExplorer();
    await pick(user, "orders.created");
    const kinds = within(screen.getByTestId("metrics-chart-kind"));
    expect(kinds.getAllByRole("button").map((b) => b.textContent)).toEqual(["Line", "Area", "Bar", "Number", "Table"]);
    await user.click(kinds.getByRole("button", { name: "Area" }));
    expect(kinds.getByRole("button", { name: "Area" })).toHaveAttribute("aria-pressed", "true");
    expect(kinds.getByRole("button", { name: "Line" })).toHaveAttribute("aria-pressed", "false");
    expect(stateFromSearch(current.search as Record<string, string>).chart).toBe("area");
    expect(screen.getByTestId("metrics-chart")).toHaveAttribute("data-kind", "line");
  });

  it("never offers an up-down counter a rate", async () => {
    const user = userEvent.setup();
    renderExplorer();
    await pick(user, "orders.in_flight");
    const agg = screen.getByTestId("metrics-query-A-agg");
    expect(agg).toHaveValue("sum");
    expect([...agg.querySelectorAll("option")].map((o) => o.value)).not.toContain("rate");
    expect([...agg.querySelectorAll("option")].map((o) => o.value)).not.toContain("increase");
  });
});

describe("the builder", () => {
  it("adds a second query and a formula over them, and refuses a formula that does not parse", async () => {
    const user = userEvent.setup();
    const { onCall } = renderExplorer();
    await pick(user, "rpc.server.calls");
    await user.click(screen.getByTestId("metrics-add-query"));
    expect(screen.getByTestId("metrics-query-B-metric")).toHaveValue("rpc.server.calls");
    await user.click(screen.getByTestId("metrics-add-formula"));
    await user.type(screen.getByTestId("metrics-formula"), "B / A +");
    expect(screen.getByTestId("metrics-formula-problem")).toBeInTheDocument();
    expect(screen.getByTestId("metrics-pick-prompt")).toHaveTextContent("Fix the formula");
    await user.type(screen.getByTestId("metrics-formula"), " 1");
    expect(screen.queryByTestId("metrics-formula-problem")).not.toBeInTheDocument();
    await waitFor(() => expect(lastPanel(onCall).formula).toBe("B / A + 1"), SLOW);
    expect(lastPanel(onCall).series.map((s) => s.label)).toEqual(["A", "B"]);
  });

  it("says when the series cap cut the result", async () => {
    const user = userEvent.setup();
    const { onCall } = renderExplorer({ seriesTotal: 1204, warnings: ["mixed_bounds"] });
    await pick(user, "checkout.latency");
    await user.click(await screen.findByTestId("metric-attr-group-status", {}, SLOW));
    await waitFor(() => expect(lastPanel(onCall).groupBy).toEqual(["metric.attr.status"]), SLOW);
    expect(await screen.findByTestId("metrics-cap-note", {}, SLOW)).toHaveTextContent("Showing 3 of 1,204 series");
    expect(screen.getByTestId("metrics-warning")).toHaveTextContent("Histogram buckets differ");
    expect(within(screen.getByTestId("metrics-series-table")).getAllByTestId("metrics-series-toggle")).toHaveLength(3);
  });

  it("hides and solos series from the table, as view state in the URL", async () => {
    const user = userEvent.setup();
    const { current } = renderExplorer();
    await pick(user, "orders.created");
    await user.click(await screen.findByTestId("metric-attr-group-status", {}, SLOW));
    const table = await screen.findByTestId("metrics-series-table", {}, SLOW);
    await waitFor(() => expect(within(table).getAllByTestId("metrics-series-toggle")).toHaveLength(3), SLOW);
    await user.click(within(table).getAllByTestId("metrics-series-solo")[1]!);
    expect(stateFromSearch(current.search as Record<string, string>).hidden).toEqual(["ok", "timeout"]);
    await user.click(within(table).getAllByTestId("metrics-series-toggle")[0]!);
    expect(stateFromSearch(current.search as Record<string, string>).hidden).toEqual(["timeout"]);
  });

  it("breaks a metric down by an attribute, and keeps one value as a filter", async () => {
    const user = userEvent.setup();
    const { onCall } = renderExplorer();
    await pick(user, "orders.created");
    await user.click(await screen.findByTestId("metric-attr-breakdown-status", {}, SLOW));
    const grid = await screen.findByTestId("metrics-breakdown-grid", {}, SLOW);
    expect([...grid.querySelectorAll("[data-value]")].map((el) => el.getAttribute("data-value"))).toEqual(["ok", "error", "timeout"]);
    await user.click(within(grid).getAllByTestId("metrics-breakdown-filter")[1]!);
    expect(screen.queryByTestId("metrics-breakdown")).not.toBeInTheDocument();
    await waitFor(() => expect(lastPanel(onCall).where).toEqual([{ field: "metric.attr.status", op: "eq", value: "error" }]), SLOW);
  });
});

describe("the chart", () => {
  it("captions the chart for a screen reader, and compares with the previous period", async () => {
    const user = userEvent.setup();
    const { onCall } = renderExplorer();
    await pick(user, "orders.created");
    const figure = await screen.findByRole("group", { name: "rate of orders.created" }, SLOW);
    expect(figure.querySelector("figcaption")).toHaveTextContent(/A: latest/);

    await user.click(within(screen.getByTestId("metrics-compare")).getByRole("button", { name: "Previous period" }));
    expect(await screen.findByRole("group", { name: "rate of orders.created, compared with the previous period" }, SLOW)).toBeInTheDocument();
    const windows = panelCalls(onCall).map((r) => r.window.endMs);
    // The comparison reads the window an hour earlier.
    expect(Math.max(...windows) - Math.min(...windows)).toBeGreaterThan(55 * 60_000);
  });
});

describe("the SQL tab", () => {
  it("shows the statements the query compiles to, and opens one as a SQL panel over the scope's parameters", async () => {
    const user = userEvent.setup();
    const { onCall, current } = renderExplorer();
    await pick(user, "orders.created");
    await user.click(screen.getByTestId("metrics-tab-sql"));
    const statements = await screen.findAllByTestId("metrics-sql-statement", {}, SLOW);
    expect(statements).toHaveLength(2);
    expect(statements[1]).toHaveTextContent("toIntervalSecond({p4:UInt32})");
    expect((calls(onCall, "QueryPanel") as QueryPanelRequest[]).some((r) => r.explain)).toBe(true);

    await user.click(within(statements[1]!).getByTestId("metrics-sql-open-panel"));
    const editor = await screen.findByTestId("metrics-sql-editor");
    const sql = (editor as HTMLTextAreaElement).value;
    expect(sql).toContain("toIntervalSecond({step:UInt32})");
    expect(sql).toContain("{to:DateTime64(9)}");
    expect(sql).toContain("({from:DateTime64(9)} - toIntervalSecond(120))");
    expect(sql).toContain("{env_ids:Array(String)}");
    expect(sql).toContain("FROM otel_metrics_sum");
    expect(sql).not.toMatch(/\{p\d+:/);
    expect(stateFromSearch(current.search as Record<string, string>).sqlPanel?.sql).toBe(sql);

    await user.click(screen.getByTestId("metrics-sql-back"));
    expect(screen.getByTestId("metrics-builder")).toBeInTheDocument();
  });
});

describe("saving to a dashboard", () => {
  async function openSave(user: ReturnType<typeof userEvent.setup>) {
    await pick(user, "checkout.latency");
    await user.click(await screen.findByTestId("metrics-save-open", {}, SLOW));
    return screen.findByTestId("metrics-save-dialog");
  }

  it("appends the panel to a workspace dashboard as its next version", async () => {
    const user = userEvent.setup();
    const { onCall } = renderExplorer();
    await openSave(user);
    await user.click(screen.getByTestId("metrics-save-target-checkout-latency"));
    await user.click(screen.getByTestId("metrics-save-confirm"));
    expect(await screen.findByTestId("metrics-save-done", {}, SLOW)).toHaveTextContent("Added p95 checkout.latency to Checkout latency as v5");
    const [saved] = calls(onCall, "SaveDashboard") as { slug: string; baseVersion: number; spec: { panels: Panel[] } }[];
    expect(saved).toMatchObject({ slug: "checkout-latency", baseVersion: 4 });
    expect(saved!.spec.panels.at(-1)).toMatchObject({ id: "p95-checkout-latency", title: "p95 checkout.latency", series: [{ field: "checkout.latency", agg: "quantile", quantile: 0.95 }] });
    expect(screen.getByTestId("metrics-save-open-dashboard")).toHaveAttribute("href", "/d/workspace/checkout-latency");
  });

  it("re-reads the dashboard and appends once more when someone saved in between", async () => {
    const user = userEvent.setup();
    const { onCall } = renderExplorer({ saveConflicts: 1 });
    await openSave(user);
    await user.click(screen.getByTestId("metrics-save-target-checkout-latency"));
    await user.click(screen.getByTestId("metrics-save-confirm"));
    expect(await screen.findByTestId("metrics-save-done", {}, SLOW)).toBeInTheDocument();
    expect(calls(onCall, "SaveDashboard")).toHaveLength(2);
    expect(calls(onCall, "GetDashboard").length).toBeGreaterThanOrEqual(2);
  });

  it("shows the conflict when it is lost twice, rather than looping", async () => {
    const user = userEvent.setup();
    const { onCall } = renderExplorer({ saveConflicts: 2 });
    await openSave(user);
    await user.click(screen.getByTestId("metrics-save-target-checkout-latency"));
    await user.click(screen.getByTestId("metrics-save-confirm"));
    expect(await screen.findByTestId("metrics-save-error", {}, SLOW)).toHaveTextContent("Someone else saved this dashboard");
    expect(calls(onCall, "SaveDashboard")).toHaveLength(2);
  });

  it("creates a new dashboard holding the panel", async () => {
    const user = userEvent.setup();
    const { onCall } = renderExplorer();
    await openSave(user);
    await user.click(screen.getByTestId("metrics-save-target-new"));
    await user.type(screen.getByTestId("metrics-save-new-title"), "Checkout health");
    await user.click(screen.getByTestId("metrics-save-confirm"));
    expect(await screen.findByTestId("metrics-save-done", {}, SLOW)).toBeInTheDocument();
    const [saved] = calls(onCall, "SaveDashboard") as { slug: string; baseVersion: number; envScope: { envIds: string[] }; spec: { panels: Panel[] } }[];
    expect(saved).toMatchObject({ slug: "checkout-health", baseVersion: 0, envScope: { envIds: ["env-prod"] } });
    expect(saved!.spec.panels).toHaveLength(1);
  });

  it("gives a non-admin the panel JSON to copy instead", async () => {
    const user = userEvent.setup();
    renderExplorer({ dashboardsReadOnly: true });
    await pick(user, "checkout.latency");
    const copy = await screen.findByTestId("metrics-copy-panel-json", {}, SLOW);
    expect(screen.queryByTestId("metrics-save-open")).not.toBeInTheDocument();
    await user.click(copy);
    const json = JSON.parse(await navigator.clipboard.readText()) as Panel;
    expect(json).toMatchObject({ kind: "line", signal: "metrics", title: "p95 checkout.latency", series: [{ field: "checkout.latency", agg: "quantile" }] });
  });
});

describe("the URL", () => {
  it("restores the page from a link: query, chart, detail and breakdown", async () => {
    const state: ExplorerState = {
      panel: {
        id: "explore",
        title: "Explore",
        kind: "table",
        signal: "metrics",
        series: [{ label: "A", agg: "rate", field: "rpc.server.calls", metricType: "sum" }],
        groupBy: ["metric.attr.status"],
      },
      chart: "table",
      compare: "off",
      hidden: [],
      detailMetric: "rpc.server.calls",
      breakdownKey: "metric.attr.rpc.method",
    };
    const { onCall } = renderExplorer({}, { ...searchFromState(state), range: "6h" });
    expect(await screen.findByTestId("metrics-query-A-metric", {}, SLOW)).toHaveValue("rpc.server.calls");
    expect(screen.getByTestId("metrics-query-A-agg")).toHaveValue("rate");
    expect(within(screen.getByTestId("metrics-chart-kind")).getByRole("button", { name: "Table" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByTestId("metrics-range")).toHaveValue("6h");
    expect(await screen.findByTestId("metric-detail", {}, SLOW)).toHaveAttribute("data-metric", "rpc.server.calls");
    expect(await screen.findByTestId("metrics-breakdown", {}, SLOW)).toHaveAttribute("data-field", "metric.attr.rpc.method");
    expect(await screen.findByTestId("obs-panel-table", {}, SLOW)).toBeInTheDocument();
    await waitFor(() => expect(lastPanel(onCall)).toMatchObject({ kind: "table", groupBy: ["metric.attr.status"] }), SLOW);
  });

  it("writes every edit back to the URL", async () => {
    const user = userEvent.setup();
    const { current } = renderExplorer();
    await pick(user, "queue.depth");
    await user.click(within(screen.getByTestId("metrics-chart-kind")).getByRole("button", { name: "Number" }));
    const back = stateFromSearch(current.search as Record<string, string>);
    expect(back.chart).toBe("number");
    expect(back.detailMetric).toBe("queue.depth");
    expect(back.panel.series[0]).toMatchObject({ field: "queue.depth", agg: "avg" });
  });
});

describe("states", () => {
  it("says too many queries are in flight, and recovers on retry", async () => {
    const user = userEvent.setup();
    const { options } = renderExplorer({ inFlight: true });
    expect(await screen.findByTestId("metrics-in-flight", {}, SLOW)).toHaveTextContent("Too many queries in flight");
    act(() => {
      options.inFlight = false;
    });
    await user.click(screen.getByTestId("metrics-in-flight-retry"));
    expect(await screen.findByTestId("metrics-catalog-list", {}, SLOW)).toBeInTheDocument();
  });

  it("says when a metric has no data in the window", async () => {
    renderExplorer({}, searchFromState({ ...stateFromSearch({}), detailMetric: "no.such.metric" }));
    expect(await screen.findByTestId("metric-no-data", {}, SLOW)).toHaveTextContent("No data for no.such.metric in the last hour");
  });

  it("says when nothing was sent in the window", async () => {
    renderExplorer({ empty: true });
    expect(await screen.findByTestId("metrics-catalog-empty", {}, SLOW)).toHaveTextContent("No metrics in the last hour");
  });

  it("says how to get started when nothing has arrived this week", async () => {
    renderExplorer({ empty: true, signals: { services: [], truncated: false } });
    const state = await screen.findByTestId("obs-nothing-yet", {}, SLOW);
    expect(within(state).getByTestId("obs-nothing-yet-about")).toHaveTextContent("Metrics charts RPC rate");
    expect(within(state).getByText("forge obs status prod")).toBeInTheDocument();
  });

  it("leads the not-set-up answer with what Metrics shows", async () => {
    renderExplorer({ failWith: Code.FailedPrecondition });
    const state = await screen.findByTestId("obs-not-set-up", {}, SLOW);
    expect(within(state).getByTestId("obs-not-set-up-about")).toHaveTextContent("Metrics charts RPC rate");
    expect(within(state).getByText("forge env deploy prod")).toBeInTheDocument();
  });
});
