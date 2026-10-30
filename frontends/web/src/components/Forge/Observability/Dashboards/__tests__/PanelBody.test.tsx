// Copyright (c) 2025 Reliant Labs

/**
 * PanelBody draws every kind of forge panel from one QueryPanel answer — the
 * browser kinds of C7 (funnel, issues, sessions; task R5) included — and
 * every state of it: loading, empty, a panel the server cannot compile, a
 * failure, and a kind this console does not know.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import type { Panel, PanelResult } from "@/services/controlPlane/observability/types";
import { quotaPausedError } from "@/services/controlPlane/observability/quota";

import { fixtureBrowserPanel, fixturePanel, FIXTURE_TRACE_ID } from "../../__fixtures__/fixtureApi";
import { alignShifted, describeChange, PanelBody } from "../PanelBody";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const HOUR = { startMs: Date.now() - 3_600_000, endMs: Date.now() };
const ready = (data: PanelResult) => ({ data, isLoading: false, error: null });

const funnel: Panel = { id: "f", title: "Checkout", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["view_product", "add_to_cart", "begin_checkout", "purchase"] } };
const issues: Panel = { id: "i", title: "Issues", kind: "issues", signal: "traces", series: [] };
const sessions: Panel = { id: "s", title: "Sessions with errors", kind: "sessions", signal: "traces", series: [], hasError: true };

function answer(panel: Panel): PanelResult {
  return fixturePanel(panel, HOUR, []);
}

describe("the browser kinds (C7, R5)", () => {
  it("draws a funnel step by step, with the overall rate and the biggest drop", () => {
    render(<PanelBody panel={funnel} query={ready(answer(funnel))} />);
    const drawn = screen.getByTestId("obs-panel-funnel");
    const steps = within(drawn).getAllByRole("listitem");
    expect(steps.map((s) => s.getAttribute("data-step"))).toEqual(funnel.funnel!.steps);
    expect(steps[1]).toHaveTextContent("46% of previous");
    expect(drawn).toHaveTextContent("journeys finished");
    expect(drawn).toHaveTextContent("the biggest drop is into begin_checkout");
  });

  it("lists issues by cause, with where, which build, how often, and a sample trace", () => {
    render(<PanelBody panel={issues} query={ready(answer(issues))} links={{ traceHref: (id) => `/traces/${id}` }} />);
    const rows = within(screen.getByTestId("obs-panel-issues")).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    expect(rows[0]).toHaveTextContent("TypeError: Cannot read properties of undefined");
    expect(rows[0]).toHaveTextContent("/checkout/payment · build v1.8.3");
    expect(within(rows[0]).getByRole("link", { name: "Open trace" })).toHaveAttribute("href", `/traces/${FIXTURE_TRACE_ID}`);
    // A UInt64 stack hash stays exact: it is text, never a JS number.
    expect(rows[0].getAttribute("data-stack")).toBe("1844674407370955162");
  });

  it("lists sessions — only those with errors when the panel says so — and marks a recorded replay", () => {
    render(<PanelBody panel={sessions} query={ready(answer(sessions))} />);
    const table = screen.getByTestId("obs-panel-sessions");
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    expect(rows[0]).toHaveTextContent("u_8812");
    expect(rows[0]).toHaveTextContent("2 errors");
    expect(within(rows[0]).getByText("replay")).toBeInTheDocument();
    expect(within(table).getAllByText("anonymous").length).toBe(1);
  });

  it("says plainly when a browser kind has nothing in the window", () => {
    const empty = { ...answer(funnel), ...fixtureBrowserPanel(funnel, HOUR, ["api"]) };
    render(<PanelBody panel={funnel} query={ready(empty)} />);
    expect(screen.getByTestId("obs-panel-empty")).toHaveTextContent("No journeys entered this funnel");
  });
});

describe("states", () => {
  const line: Panel = { id: "l", title: "Requests", kind: "line", signal: "traces", series: [{ label: "requests", agg: "count" }] };

  it("loads, fails with the server's reason, and refuses a kind it does not know", () => {
    const { rerender } = render(<PanelBody panel={line} query={{ data: undefined, isLoading: true, error: null }} />);
    expect(screen.getByLabelText("Loading")).toBeInTheDocument();
    rerender(<PanelBody panel={line} query={{ data: undefined, isLoading: false, error: new ConnectError('unknown field "span.colour"', Code.InvalidArgument) }} />);
    expect(screen.getByTestId("obs-panel-unsupported")).toHaveTextContent('unknown field "span.colour"');
    rerender(<PanelBody panel={line} query={{ data: undefined, isLoading: false, error: new ConnectError("down", Code.Unavailable) }} />);
    expect(screen.getByTestId("obs-panel-error")).toBeInTheDocument();
    rerender(<PanelBody panel={{ ...line, kind: "heatmap" }} query={ready(answer(line))} />);
    expect(screen.getByTestId("obs-panel-unknown-kind")).toHaveTextContent("heatmap");
  });

  it("keeps the last panel result on quota exhaustion, and shows a neutral wait when none loaded", () => {
    const data = answer(line);
    const error = quotaPausedError({ limit: "queries", lane: "interactive", resetsAtMs: Date.now() + 60_000, soft: false });
    const { rerender } = render(<PanelBody panel={line} query={{ data, isLoading: false, error }} />);
    expect(screen.getByRole("group", { name: "Requests" })).toHaveTextContent("requests: latest");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    rerender(<PanelBody panel={line} query={{ data: undefined, isLoading: false, error }} />);
    expect(screen.getByTestId("obs-error-over-quota")).toHaveAttribute("role", "status");
    expect(screen.getByTestId("obs-error-over-quota")).toHaveTextContent("Will load at");
    expect(screen.queryByTestId("obs-panel-error")).not.toBeInTheDocument();
  });

  it("says it is waiting for capacity, not that it failed, while the server's cap refuses it and it retries", () => {
    const cap = new ConnectError("too many observability queries in flight; retry shortly", Code.ResourceExhausted, { "x-forge-error-reason": "in_flight_limit" });
    const { rerender } = render(<PanelBody panel={line} query={{ data: undefined, isLoading: true, error: null, failureCount: 2, failureReason: cap }} />);
    expect(screen.getByTestId("obs-panel-waiting")).toHaveTextContent("Queued…");
    expect(screen.queryByTestId("obs-panel-error")).not.toBeInTheDocument();
    // A different failure being retried is still just loading.
    rerender(<PanelBody panel={line} query={{ data: undefined, isLoading: true, error: null, failureCount: 1, failureReason: new ConnectError("down", Code.Unavailable) }} />);
    expect(screen.queryByTestId("obs-panel-waiting")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Loading")).toBeInTheDocument();
  });

  it("offers Retry on every failure that is not a deliberate answer, and Retry reads this panel again", async () => {
    const user = userEvent.setup();
    const refetch = vi.fn();
    const { rerender } = render(<PanelBody panel={line} query={{ data: undefined, isLoading: false, error: new ConnectError("too many observability queries in flight; retry shortly", Code.ResourceExhausted), refetch }} />);
    expect(screen.getByTestId("obs-panel-error")).toHaveTextContent("This panel couldn't be loaded. too many observability queries in flight; retry shortly");
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(refetch).toHaveBeenCalledTimes(1);

    rerender(<PanelBody panel={line} query={{ data: undefined, isLoading: false, error: new ConnectError("down", Code.Unavailable), refetch }} />);
    expect(screen.getByRole("button", { name: "Retry" })).toBeEnabled();
    rerender(<PanelBody panel={line} query={{ data: undefined, isLoading: false, error: new ConnectError("down", Code.Unavailable), refetch, isFetching: true }} />);
    expect(screen.getByRole("button", { name: "Retry" })).toBeDisabled();
    // A panel the server cannot compile is not helped by asking again.
    rerender(<PanelBody panel={line} query={{ data: undefined, isLoading: false, error: new ConnectError("unknown field", Code.InvalidArgument), refetch }} />);
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
  });

  it("draws a metric with no data in the scope as empty, naming the metric and where", () => {
    const metrics: Panel = { id: "m", title: "Routes", kind: "line", signal: "metrics", series: [{ label: "routes", agg: "max", field: "gnatsd_routez_num_routes", metricType: "gauge" }] };
    const none: PanelResult = {
      ...answer(line),
      series: [],
      rows: [],
      warnings: ["step_raised: 30s requested, 60s used", "metric_absent: gnatsd_routez_num_routes has no data in the selected environments in this time range"],
    };
    render(<PanelBody panel={metrics} query={ready(none)} />);
    expect(screen.queryByTestId("obs-panel-error")).not.toBeInTheDocument();
    expect(screen.getByTestId("obs-panel-empty")).toHaveTextContent("No data in this window");
    expect(screen.getByTestId("obs-panel-absent")).toHaveTextContent("gnatsd_routez_num_routes has no data in the selected environments in this time range");
    expect(screen.getByTestId("obs-panel-absent")).not.toHaveTextContent("step_raised");
  });

  it("opens the drill-down from a line's legend and a table's row", async () => {
    const user = userEvent.setup();
    const onDrill = vi.fn();
    const grouped: Panel = { ...line, groupBy: ["span.name"] };
    const { rerender } = render(<PanelBody panel={grouped} query={ready(answer(grouped))} links={{ onDrill }} />);
    await user.click(within(screen.getByTestId("obs-panel-legend")).getAllByRole("button")[0]);
    expect(onDrill).toHaveBeenLastCalledWith(expect.objectContaining({ group: { "span.name": "orders.v1.OrdersService/CreateOrder" } }), expect.anything());
    const table: Panel = { ...grouped, kind: "table" };
    rerender(<PanelBody panel={table} query={ready(answer(table))} links={{ onDrill }} />);
    await user.click(within(within(screen.getByTestId("obs-panel-table")).getAllByRole("row")[2]).getByRole("button"));
    expect(onDrill).toHaveBeenLastCalledWith(expect.objectContaining({ group: { "span.name": "orders.v1.OrdersService/GetOrder" } }), expect.anything());
  });

  it("states a number's change against the comparison", () => {
    const number: Panel = { id: "p95", title: "p95", kind: "number", signal: "traces", unit: "ms", series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }] };
    const now = answer(number);
    const before = { ...now, series: now.series.map((s) => ({ ...s, value: (s.value ?? 0) - 18 })) };
    render(<PanelBody panel={number} query={ready(now)} compare={{ query: ready(before), label: "previous period", offsetSec: 3600 }} />);
    expect(screen.getByTestId("obs-panel-change")).toHaveTextContent("▲ 18ms vs previous period");
  });
});

describe("helpers", () => {
  it("phrases changes by unit", () => {
    expect(describeChange(0.012, 0.01, "percent", "previous period")).toBe("▲ 0.2pt vs previous period");
    expect(describeChange(90, 100, "count", "previous release")).toBe("▼ 10% vs previous release");
    expect(describeChange(5, 5, "count", "x")).toBe("no change vs x");
    expect(describeChange(null, 5, "count", "x")).toBeNull();
  });

  it("lines a shifted comparison up with the current buckets, to the nearest half step", () => {
    expect(alignShifted([1000, 1060, 1120], { timestamps: [-2600, -2540, -2480], values: [1, 2, 3] }, 3605)).toEqual([1, 2, 3]);
    expect(alignShifted([1000, 1060], { timestamps: [0], values: [9] }, 1070)).toEqual([null, 9]);
  });
});
