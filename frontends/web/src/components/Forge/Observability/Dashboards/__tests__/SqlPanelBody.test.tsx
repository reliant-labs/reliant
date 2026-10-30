// Copyright (c) 2025 Reliant Labs

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

import type { Panel, PanelResult } from "@/services/controlPlane/observability/types";
import { UPlotChart } from "../../charts/UPlotChart";
import { PanelBody } from "../PanelBody";

vi.mock("../../charts/UPlotChart", () => ({ UPlotChart: vi.fn(() => <div data-testid="sql-chart" />) }));

// Result aliases/shapes from deploy/observability/dashboards/product-*.json
// in control-plane. Keep these local: the console's tests must not depend on
// having the sibling checkout. SQL specs declare neither series nor groupBy.
const panel = (id: string, unit?: Panel["unit"]): Panel => ({ id, title: id, kind: "sql", signal: "traces", series: [], unit });
const result = (data: Partial<PanelResult> = {}): PanelResult => ({
  kind: "sql", unit: "", series: [], columns: [], rows: [], stepSeconds: 0,
  truncated: false, funnelSteps: [], issues: [], sessions: [], seriesMeta: [],
  seriesTotal: 0, warnings: [], compiledSql: [], ...data,
});
const ready = (data: PanelResult) => ({ data, isLoading: false, error: null });
const row = (group: Record<string, string>, values: (number | null)[]) => ({ group, values });

const tables = [
  { dashboard: "active-users", id: "active-by-version", columns: ["users", "events"], rows: [row({ service: "reliant", version: "v1.2" }, [12, 120])] },
  { dashboard: "retention", id: "signup-cohorts", columns: ["users", "w1", "w2", "w3", "w4", "w5", "w6", "w7", "w8"], rows: [row({ cohort_week: "2026-10-05" }, [100, 50, 40, 30, 20, 15, 10, 5, 0])] },
  { dashboard: "funnel", id: "stitched-funnel", columns: ["users", "pct_of_first", "pct_of_previous"], rows: [row({ label: "1. Landing" }, [100, 100, 100]), row({ label: "2. Download" }, [20, 20, 20])] },
  // A string result column after the numeric columns is still a group key;
  // Columns contains ONLY the numeric aliases, not every SQL select column.
  { dashboard: "usage", id: "event-mix", columns: ["events", "users"], rows: [row({ event: "page_visited", clients: "electron,web" }, [120, 12])] },
  { dashboard: "monetization", id: "top-coupons", columns: ["redemptions", "orgs", "credit_usd", "compute_minutes"], rows: [row({ coupon: "WELCOME", kind: "credit" }, [10, 8, 25, 60])] },
  { dashboard: "errors", id: "top-users-procedures", columns: ["errors"], rows: [row({ user: "u_123", procedure: "ChatService/Send" }, [7])] },
];

describe("SQL result tables", () => {
  it.each(tables)("renders $dashboard/$id from response columns and group attributes", ({ id, columns, rows }) => {
    render(<PanelBody panel={panel(id)} query={ready(result({ columns, rows }))} />);
    const table = screen.getByTestId("obs-panel-table");
    const groups = Object.keys(rows[0].group);
    expect(within(table).getAllByRole("columnheader").map((cell) => cell.textContent)).toEqual([...groups, ...columns]);
    const bodyRows = within(table).getAllByRole("row").slice(1);
    expect(bodyRows).toHaveLength(rows.length);
    rows.forEach((r, index) => {
      expect(Array.from(bodyRows[index].children).map((cell) => cell.textContent)).toEqual([
        ...Object.values(r.group), ...r.values.map(String),
      ]);
    });
    expect(screen.queryByTestId("sql-chart")).not.toBeInTheDocument();
    expect(screen.queryByTestId("obs-panel-number")).not.toBeInTheDocument();
  });

  it("unions keys from every row, ignores spec groupBy, and preserves missing cells", () => {
    render(<PanelBody panel={{ ...panel("rows"), groupBy: ["not_a_sql_column"] }} groupBy={["also_not_a_column"]} query={ready(result({
      columns: ["events"], rows: [row({ event: "first" }, [1]), row({ event: "second", clients: "web" }, [2])],
    }))} />);
    const table = screen.getByTestId("obs-panel-table");
    expect(within(table).getAllByRole("columnheader").map((cell) => cell.textContent)).toEqual(["event", "clients", "events"]);
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("first—1");
    expect(within(table).getAllByRole("row")[2]).toHaveTextContent("secondweb2");
  });

  it("keeps multiple numeric-only rows as a table instead of discarding rows as an aggregate", () => {
    render(<PanelBody panel={panel("rows")} query={ready(result({ columns: ["n"], rows: [row({}, [1]), row({}, [2])] }))} />);
    expect(within(screen.getByTestId("obs-panel-table")).getAllByRole("row")).toHaveLength(3);
    expect(screen.queryByTestId("obs-panel-number")).not.toBeInTheDocument();
  });

  it("renders string-only rows instead of an empty chart", () => {
    render(<PanelBody panel={panel("text")} query={ready(result({ rows: [row({ name: "hello" }, [])] }))} />);
    expect(screen.getByTestId("obs-panel-table")).toHaveTextContent("hello");
  });

  it("caps compact previews at three rows but derives headers from the full result and hides truncation", () => {
    const data = result({ columns: ["events"], truncated: true, rows: [row({ event: "one" }, [1]), row({ event: "two" }, [2]), row({ event: "three" }, [3]), row({ event: "four", clients: "web" }, [4])] });
    const { rerender } = render(<PanelBody panel={panel("event-mix")} query={ready(data)} />);
    expect(screen.getByText("Only the first returned rows are shown.")).toBeInTheDocument();
    expect(within(screen.getByTestId("obs-panel-table")).getAllByRole("row")).toHaveLength(5);
    rerender(<PanelBody panel={panel("event-mix")} query={ready(data)} compact />);
    const table = screen.getByTestId("obs-panel-table");
    expect(within(table).getAllByRole("row")).toHaveLength(4);
    expect(within(table).getByRole("columnheader", { name: "clients" })).toBeInTheDocument();
    expect(screen.queryByText(/Only the/)).not.toBeInTheDocument();
    expect(screen.queryByText("four")).not.toBeInTheDocument();
  });

  it("does not mistake a truncated single numeric row for an aggregate", () => {
    render(<PanelBody panel={panel("capped")} query={ready(result({ columns: ["n"], rows: [row({}, [1])], truncated: true }))} />);
    expect(screen.getByTestId("obs-panel-table")).toHaveTextContent("1");
    expect(screen.getByText(/first returned rows/)).toBeInTheDocument();
  });
});

describe("SQL aggregate figures", () => {
  it.each([
    { id: "active-now", columns: ["dau", "wau", "mau", "stickiness_pct"], values: [12, 60, 100, 12] },
    { id: "churn-snapshot", columns: ["retained", "churned", "new_users", "resurrected", "churn_pct"], values: [20, 5, 8, 2, 20] },
    { id: "errors-headline", columns: ["errors", "server_errors", "browser_errors", "users_with_errors", "active_users", "attributed_pct", "errors_per_active_user"], values: [10, 7, 3, 4, 20, 100, 0.5] },
    { id: "one-number", columns: ["total"], values: [0] },
  ])("names every $id figure from the response", ({ id, columns, values }) => {
    render(<PanelBody panel={panel(id)} query={ready(result({ columns, rows: [row({}, values)] }))} />);
    const figures = screen.getByTestId("obs-panel-number");
    expect(Array.from(figures.querySelectorAll("dt")).map((label) => label.textContent)).toEqual(columns);
    expect(Array.from(figures.querySelectorAll("dd")).map((value) => value.textContent)).toEqual(values.map(String));
    expect(screen.queryByTestId("obs-panel-table")).not.toBeInTheDocument();
  });

  it("keeps null aggregates distinct from zero and does not invent comparisons", () => {
    const data = result({ columns: ["zero", "missing", "negative"], rows: [row({}, [0, null, -2])] });
    const { rerender } = render(<PanelBody panel={panel("nullable")} query={ready(data)} compare={{ query: ready(result({ columns: ["missing", "zero", "negative"], rows: [row({}, [5, 0, null])] })), label: "before", offsetSec: 3600 }} />);
    const figures = screen.getByTestId("obs-panel-number");
    expect(Array.from(figures.children).map((figure) => within(figure as HTMLElement).getAllByRole("definition")[0].textContent)).toEqual(["0", "—", "-2"]);
    expect(screen.getAllByTestId("obs-panel-change")).toHaveLength(1);
    expect(screen.getByTestId("obs-panel-change")).toHaveTextContent("no change vs before");
    rerender(<PanelBody panel={panel("nullable")} query={ready(result({ columns: ["missing"], rows: [row({}, [null])] }))} />);
    expect(screen.getByTestId("obs-panel-number")).toHaveTextContent("—");
    expect(screen.queryByTestId("obs-panel-empty")).not.toBeInTheDocument();
  });

  it("reuses units, comparisons and compact figures without guessing units from aliases", () => {
    const data = result({ columns: ["share"], rows: [row({}, [0.25])] });
    render(<PanelBody panel={panel("share", "percent")} query={ready(data)} compact compare={{ query: ready(result({ columns: ["share"], rows: [row({}, [0.2])] })), label: "previous period", offsetSec: 3600 }} />);
    expect(screen.getByTestId("obs-panel-number")).toHaveTextContent("25%");
    expect(screen.getByTestId("obs-panel-change")).toHaveTextContent("▲ 5pt vs previous period");
  });
});

describe("SQL time series", () => {
  it.each<{ id: string; labels: string[]; group: Record<string, string> }>([
    { id: "dau-wau-mau", labels: ["dau", "wau", "mau"], group: {} },
    { id: "landing-daily", labels: ["visitors", "clicked_download_or_app", "signed_in"], group: {} },
    { id: "coupons", labels: ["redemptions", "credit_usd", "compute_minutes"], group: { kind: "credit" } },
    { id: "errors-by-client", labels: ["errors"], group: { Client: "backend" } },
  ])("draws $id using returned series labels, not the empty panel.series", ({ id, labels, group }) => {
    const data = result({ series: labels.map((label) => ({ label, group, timestamps: [1000, 1060], values: [1, 2], value: null })) });
    const { rerender } = render(<PanelBody panel={panel(id)} query={ready(data)} />);
    expect(screen.getByTestId("sql-chart")).toBeInTheDocument();
    expect(screen.queryByTestId("obs-panel-table")).not.toBeInTheDocument();
    const expected = labels.map((label) => ({ label: Object.values(group).length ? `${Object.values(group).join(" · ")} — ${label}` : label, values: [1, 2] }));
    expect(vi.mocked(UPlotChart).mock.lastCall?.[0]).toMatchObject({ timestamps: [1000, 1060], series: expected, compact: false });
    rerender(<PanelBody panel={panel(id)} query={ready(data)} compact />);
    expect(vi.mocked(UPlotChart).mock.lastCall?.[0]).toMatchObject({ compact: true, height: 28, legend: false });
  });

  it("aligns sparse groups without losing zero, null gaps or a ratio alias", () => {
    const series: Array<{ label: string; group: Record<string, string>; timestamps: number[]; values: (number | null)[]; value: null }> = [
      { label: "ratio", group: { client: "web" }, timestamps: [1000, 1120], values: [0, null], value: null },
      { label: "users", group: { client: "web", version: "v2" }, timestamps: [1060], values: [2], value: null },
    ];
    render(<PanelBody panel={panel("sparse")} query={ready(result({ series }))} />);
    expect(vi.mocked(UPlotChart).mock.lastCall?.[0]).toMatchObject({
      timestamps: [1000, 1060, 1120],
      series: [
        { label: "web — ratio", values: [0, null, null] },
        { label: "web · v2 — users", values: [null, 2, null] },
      ],
    });
  });

  it("keeps both numeric column names in grouped legends and matches comparisons", () => {
    const series = ["redemptions", "credit_usd"].map((label) => ({ label, group: { kind: "credit" }, timestamps: [1000], values: [2], value: null }));
    render(<PanelBody panel={panel("coupons")} query={ready(result({ series, truncated: true }))} compare={{ query: ready(result({ series: series.map((s) => ({ ...s, timestamps: [-2600], values: [1] })) })), offsetSec: 3600, label: "before" }} />);
    expect(vi.mocked(UPlotChart).mock.lastCall?.[0].series).toMatchObject([
      { label: "credit — redemptions", values: [2] }, { label: "credit — credit_usd", values: [2] },
      { label: "credit — redemptions (before)", values: [1], dashed: true }, { label: "credit — credit_usd (before)", values: [1], dashed: true },
    ]);
    expect(screen.getByText(/first returned series or points/)).toBeInTheDocument();
  });
});

describe("SQL shared states", () => {
  it("preserves loading, empty, invalid SQL and query failures, including compact previews", () => {
    const spec = panel("active-now");
    const { rerender } = render(<PanelBody panel={spec} query={{ data: undefined, isLoading: true, error: null }} />);
    expect(screen.getByLabelText("Loading")).toHaveAttribute("aria-busy", "true");
    rerender(<PanelBody panel={spec} query={ready(result())} />);
    expect(screen.getByTestId("obs-panel-empty")).toHaveTextContent("No data in this window");
    rerender(<PanelBody panel={spec} query={ready(result())} compact />);
    expect(screen.getByText("No data")).toBeInTheDocument();
    rerender(<PanelBody panel={spec} query={{ data: undefined, isLoading: false, error: new ConnectError("unknown column", Code.InvalidArgument) }} />);
    expect(screen.getByTestId("obs-panel-unsupported")).toHaveTextContent("unknown column");
    const failed = { data: undefined, isLoading: false, error: new ConnectError("down", Code.Unavailable) };
    rerender(<PanelBody panel={spec} query={failed} />);
    expect(screen.getByRole("alert")).toHaveTextContent("down");
    rerender(<PanelBody panel={spec} query={failed} compact />);
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("SQL time series: the window asked for and the bucket's calendar", () => {
  const day = Date.UTC(2026, 9, 29) / 1000;
  const hour = { startMs: Date.UTC(2026, 9, 29, 18, 11, 39), endMs: Date.UTC(2026, 9, 29, 19, 11, 39) };
  const series = (n: number) => ({ label: "runs", group: { outcome: "success" }, timestamps: [day], values: [n], value: null });
  const lastChart = () => vi.mocked(UPlotChart).mock.calls.at(-1)![0];

  it("hands the chart the window, so one bucket is one dot placed in it", () => {
    // The audit's case: 53 runs in the last hour, bucketed per day, one point.
    render(<PanelBody panel={panel("workflows-by-outcome")} query={ready(result({ series: [series(53)], window: hour, bucketSeconds: 86_400, bucketTimezone: "UTC" }))} />);
    expect(screen.getByTestId("sql-chart")).toBeInTheDocument();
    expect(lastChart().window).toEqual({ startSec: hour.startMs / 1000, endSec: hour.endMs / 1000 });
  });

  it("marks a Date-bucketed result as UTC calendar days with the server's bucket width, and an instant series as neither", () => {
    render(<PanelBody panel={panel("workflows-by-outcome")} query={ready(result({ series: [series(53)], window: hour, bucketSeconds: 86_400, bucketTimezone: "UTC" }))} />);
    expect(lastChart().calendar).toEqual({ timezone: "UTC", bucketSeconds: 86_400 });

    render(<PanelBody panel={panel("events-by-step")} query={ready(result({ series: [series(5)], window: hour, bucketSeconds: 120 }))} />);
    expect(lastChart().calendar).toBeUndefined();
    expect(lastChart().window).toBeDefined();
  });

  it("leaves the chart on its data extent when the answer carries no window", () => {
    render(<PanelBody panel={panel("old")} query={ready(result({ series: [series(5)] }))} />);
    expect(lastChart().window).toBeUndefined();
  });
});
