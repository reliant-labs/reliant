// Copyright (c) 2025 Reliant Labs

/**
 * The charts a panel can be beyond a plain line: the legend of a busy grouped
 * line (swatches, figures, highlight, isolate, top N and "Other", the explorer
 * link), display styles, bars, pie and donut, gauge, stat sparkline, heatmap,
 * text, and a sql panel's rows as a funnel.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { Panel, PanelResult, Series } from "@/services/controlPlane/observability/types";

import { UPlotChart, type UPlotChartProps } from "../../charts/UPlotChart";
import { PanelBody } from "../PanelBody";
import { CHART_PALETTE, DANGER_PALETTE, OTHER_TOKEN } from "../seriesModel";

vi.mock("../../charts/UPlotChart", () => ({ UPlotChart: vi.fn(() => <div data-testid="chart" />) }));

const TIMES = [1000, 1060, 1120];
const result = (data: Partial<PanelResult> = {}): PanelResult => ({
  kind: "",
  unit: "",
  series: [],
  columns: [],
  rows: [],
  stepSeconds: 60,
  truncated: false,
  funnelSteps: [],
  issues: [],
  sessions: [],
  seriesMeta: [],
  seriesTotal: 0,
  warnings: [],
  compiledSql: [],
  ...data,
});
const ready = (data: PanelResult) => ({ data, isLoading: false, error: null });
const lastChart = (): UPlotChartProps => vi.mocked(UPlotChart).mock.lastCall![0] as UPlotChartProps;

const results = ["success", "requeue_after", "error"];
const controllers = ["kustomization", "helmrelease", "gitrepository", "ocirepository"];
/** 12 series; kustomization·success is the largest, everything else shrinks in order. */
const busy: Series[] = controllers.flatMap((controller, c) =>
  results.map((r, k) => ({
    label: "reconciles",
    group: { "metric.attr.controller": controller, "metric.attr.result": r },
    timestamps: TIMES,
    values: [100 - c * 20 - k * 5, 100 - c * 20 - k * 5, 100 - c * 20 - k * 5],
    value: null,
  }))
);
const reconciles: Panel = {
  id: "r",
  title: "Reconciles by result",
  kind: "line",
  signal: "metrics",
  groupBy: ["metric.attr.controller", "metric.attr.result"],
  series: [{ label: "reconciles", agg: "sum", field: "controller_runtime_reconcile_total", metricType: "sum" }],
};

beforeEach(() => vi.mocked(UPlotChart).mockClear());

describe("a busy grouped line", () => {
  it("draws the top six (the palette's size) in the result's order, folds the rest into a summed Other, and paints errors in danger", () => {
    render(<PanelBody panel={reconciles} query={ready(result({ series: busy }))} links={{ onDrill: vi.fn() }} />);
    const drawn = lastChart().series;
    expect(drawn).toHaveLength(8);
    // Ranked by total, the tail is gitrepository's three (60, 55, 50) and ocirepository's three (40, 35, 30);
    // its error series are never folded into grey: they are "Other errors", in danger.
    expect(drawn[6]).toMatchObject({ label: "Other (4 series)", color: OTHER_TOKEN, values: [190, 190, 190] });
    expect(drawn[7]).toMatchObject({ label: "Other errors (2 series)", color: DANGER_PALETTE[0], values: [80, 80, 80] });
    // Errors are red; nothing else is.
    const tokens = drawn.slice(0, 6).map((s) => [s.label, s.color]);
    for (const [label, color] of tokens) expect(DANGER_PALETTE.includes(color as never), String(label)).toBe(String(label).endsWith("error"));
    expect(drawn.slice(0, 6).every((s) => [...CHART_PALETTE, ...DANGER_PALETTE].includes(s.color as never))).toBe(true);
    // The legend replaces uPlot's.
    expect(lastChart().legend).toBe(false);

    const legend = screen.getByTestId("obs-panel-legend");
    const rows = within(legend).getAllByRole("row").slice(1);
    expect(rows[0]).toHaveAttribute("data-series", "kustomization · success");
    expect(rows[0]).toHaveTextContent("300");
    expect(within(legend).getAllByTestId("obs-panel-legend-other").map((r) => r.textContent)).toEqual([expect.stringContaining("Other · 4 series"), expect.stringContaining("Other errors · 2 series")]);
    expect(within(legend).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["12 series", "Last", "Max", "Total", "Open in explorer"]);
  });

  it("shows every series behind Show all, and back", async () => {
    const user = userEvent.setup();
    render(<PanelBody panel={reconciles} query={ready(result({ series: busy }))} />);
    await user.click(screen.getByTestId("obs-panel-legend-show-all"));
    expect(lastChart().series).toHaveLength(12);
    expect(screen.queryByTestId("obs-panel-legend-other")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show the top 6" }));
    expect(lastChart().series).toHaveLength(8);
  });

  it("highlights a series on hover, isolates it on click, adds one with ⌘-click, and resets", async () => {
    const user = userEvent.setup();
    render(<PanelBody panel={reconciles} query={ready(result({ series: busy }))} />);
    const legend = screen.getByTestId("obs-panel-legend");
    const row = (label: string) => within(legend).getAllByRole("row").find((r) => r.getAttribute("data-series") === label)!;
    const button = (label: string) => within(row(label)).getAllByRole("button")[0];

    await user.hover(row("helmrelease · error"));
    const highlighted = lastChart().highlight!;
    expect(lastChart().series[highlighted].label).toBe("helmrelease · error");

    await user.click(button("helmrelease · error"));
    expect(lastChart().series.map((s) => s.label)).toEqual(["helmrelease · error"]);
    expect(button("helmrelease · error")).toHaveAttribute("aria-pressed", "true");

    await user.keyboard("{Meta>}");
    await user.click(button("kustomization · success"));
    await user.keyboard("{/Meta}");
    expect(lastChart().series.map((s) => s.label).sort()).toEqual(["helmrelease · error", "kustomization · success"]);

    await user.click(screen.getByTestId("obs-panel-legend-reset"));
    expect(lastChart().series).toHaveLength(8);
  });

  it("does not sum what does not add up: a p95's tail is left out and named, not drawn as Other", () => {
    const p95: Panel = { ...reconciles, signal: "traces", unit: "ms", series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }] };
    render(<PanelBody panel={p95} query={ready(result({ series: busy.map((s) => ({ ...s, label: "p95" })) }))} />);
    // The top 6 by last value, plus the tail's errors drawn as themselves (they cannot be summed).
    expect(lastChart().series).toHaveLength(8);
    expect(lastChart().series.map((s) => s.label)).toEqual(expect.arrayContaining(["gitrepository · error", "ocirepository · error"]));
    expect(screen.getByTestId("obs-panel-legend-other")).toHaveTextContent("Other · 4 series (not drawn)");
    expect(within(screen.getByTestId("obs-panel-legend")).getAllByRole("columnheader").map((h) => h.textContent)).toContain("Avg");
  });

  it("honours display: area, bars, stack and topN, and an unknown style draws a line", () => {
    const { rerender } = render(<PanelBody panel={{ ...reconciles, display: { style: "area", stack: true, topN: 3 } }} query={ready(result({ series: busy }))} />);
    expect(lastChart()).toMatchObject({ area: "stacked", bars: false });
    // The top 3, "Other" and "Other errors".
    expect(lastChart().series).toHaveLength(5);
    rerender(<PanelBody panel={{ ...reconciles, display: { style: "bars" } }} query={ready(result({ series: busy }))} />);
    expect(lastChart()).toMatchObject({ area: undefined, bars: true });
    rerender(<PanelBody panel={{ ...reconciles, display: { style: "area" } }} query={ready(result({ series: busy }))} />);
    expect(lastChart()).toMatchObject({ area: "fill" });
    rerender(<PanelBody panel={{ ...reconciles, display: { style: "sparkles", stack: true } }} query={ready(result({ series: busy }))} />);
    expect(lastChart()).toMatchObject({ area: undefined, bars: false });
  });

  it("keeps the explorer link on every row", async () => {
    const user = userEvent.setup();
    const onDrill = vi.fn();
    render(<PanelBody panel={reconciles} query={ready(result({ series: busy }))} links={{ onDrill }} />);
    await user.click(screen.getByRole("button", { name: "Open helmrelease · error in the explorer" }));
    expect(onDrill).toHaveBeenCalledWith(expect.objectContaining({ group: { "metric.attr.controller": "helmrelease", "metric.attr.result": "error" } }), expect.anything());
  });
});

describe("bars, pies and gauges", () => {
  const bar: Panel = { id: "b", title: "By result", kind: "bar", signal: "metrics", groupBy: ["metric.attr.result"], series: [{ label: "n", agg: "sum", field: "m", metricType: "sum" }] };
  const rows = [["success", 50], ["requeue", 30], ["error", 20]].map(([r, n]) => ({ group: { "metric.attr.result": String(r) }, values: [Number(n)] }));

  it("draws one bar per group, the failure group in danger, and drills from a bar", async () => {
    const user = userEvent.setup();
    const onDrill = vi.fn();
    render(<PanelBody panel={bar} query={ready(result({ columns: ["n"], rows }))} links={{ onDrill }} />);
    const chart = screen.getByTestId("obs-panel-bar");
    expect(chart).toHaveAttribute("data-orientation", "vertical");
    const groups = within(chart).getAllByRole("listitem");
    expect(groups.map((g) => g.getAttribute("data-group"))).toEqual(["success", "requeue", "error"]);
    expect((groups[2].querySelector("[title]") as HTMLElement).style.backgroundColor).toContain("--destructive");
    await user.click(within(groups[1]).getByRole("button"));
    expect(onDrill).toHaveBeenCalledWith(expect.objectContaining({ group: { "metric.attr.result": "requeue" } }), expect.anything());
  });

  it("lays bars along the y axis when horizontal", () => {
    render(<PanelBody panel={{ ...bar, display: { horizontal: true } }} query={ready(result({ columns: ["n"], rows }))} />);
    expect(screen.getByTestId("obs-panel-bar")).toHaveAttribute("data-orientation", "horizontal");
  });

  it("draws a donut with its total and each slice's share, the tail folded into Other", () => {
    const many = Array.from({ length: 6 }, (_, i) => ({ group: { "service.name": `s${i}` }, values: [60 - i * 10] }));
    render(<PanelBody panel={{ ...bar, groupBy: ["service.name"], display: { style: "donut", topN: 3 } }} query={ready(result({ columns: ["n"], rows: many }))} />);
    const pie = screen.getByTestId("obs-panel-pie");
    expect(pie).toHaveAttribute("data-style", "donut");
    expect(pie).toHaveTextContent("210total");
    const slices = within(pie).getAllByRole("row");
    expect(slices.map((s) => s.getAttribute("data-slice"))).toEqual(["s0", "s1", "s2", "Other (3)"]);
    expect(slices[3]).toHaveTextContent("60");
  });

  it("draws a gauge on its scale, graded by the series' thresholds", () => {
    const lcp: Panel = { id: "g", title: "LCP", kind: "number", signal: "traces", unit: "ms", series: [{ label: "lcp", agg: "quantile", quantile: 0.75, field: "span.attr.lcp", thresholds: { good: 2500, poor: 4000 } }], display: { style: "gauge", max: 6000 } };
    render(<PanelBody panel={lcp} query={ready(result({ series: [{ label: "lcp", group: {}, timestamps: [], values: [], value: 4500 }] }))} />);
    const meter = screen.getByRole("meter", { name: "lcp" });
    expect(meter).toHaveAttribute("aria-valuemax", "6000");
    expect(meter).toHaveAttribute("aria-valuenow", "4500");
    expect(within(meter).getByText("4.5s")).toHaveClass("text-danger-ink");
  });

  it("draws a stat's sparkline from the trend the server sends with an ungrouped number", () => {
    const n: Panel = { id: "n", title: "Requests", kind: "number", signal: "traces", series: [{ label: "requests", agg: "count" }] };
    render(<PanelBody panel={n} query={ready(result({ series: [{ label: "requests", group: {}, timestamps: TIMES, values: [1, 3, 2], value: 6 }] }))} />);
    expect(screen.getByTestId("obs-panel-sparkline")).toBeInTheDocument();
    expect(lastChart()).toMatchObject({ compact: true, timestamps: TIMES });
  });
});

describe("heatmap, text and funnels", () => {
  it("draws heatmap cells, empty buckets kept to scale", () => {
    const heat: Panel = { id: "h", title: "Latency", kind: "heatmap", signal: "traces", unit: "ms", series: [{ label: "n", agg: "count", field: "span.duration" }] };
    const cells = [
      { timeSec: 960, low: 10, high: 20, count: 4 },
      { timeSec: 1080, low: 20, high: 50, count: 40 },
    ];
    render(<PanelBody panel={heat} query={ready(result({ heatmap: cells, window: { startMs: 960_000, endMs: 1_200_000 } }))} />);
    const figure = screen.getByTestId("obs-panel-heatmap");
    const grid = figure.querySelector("[style*='grid-template-columns']") as HTMLElement;
    expect(grid.style.gridTemplateColumns).toContain("repeat(4,");
    expect(grid.children).toHaveLength(2);
    expect(figure).toHaveTextContent(/busiest cell is 20ms–50ms/);
  });

  it("renders a text panel's Markdown without querying, and drops raw HTML", () => {
    const text: Panel = { id: "t", title: "About", kind: "text", signal: "", series: [], markdown: "Flux **reconciles**. <script>alert(1)</script>[Runbook](https://example.com)" };
    render(<PanelBody panel={text} query={{ data: undefined, isLoading: true, error: null }} />);
    const body = screen.getByTestId("obs-panel-text");
    expect(within(body).getByText("reconciles").tagName).toBe("STRONG");
    expect(within(body).getByRole("link", { name: "Runbook" })).toHaveAttribute("target", "_blank");
    expect(body.querySelector("script")).toBeNull();
  });

  it("draws a sql panel's rows as a funnel with display.style funnel", () => {
    const sql: Panel = { id: "s", title: "Stitched", kind: "sql", signal: "", series: [], sql: "SELECT 1", display: { style: "funnel" } };
    const rows = [["1 visited", 1000], ["2 signed up", 250], ["3 first message", 200]].map(([label, n]) => ({ group: { label: String(label) }, values: [Number(n)] }));
    render(<PanelBody panel={sql} query={ready(result({ columns: ["users"], rows }))} />);
    const funnel = screen.getByTestId("obs-panel-funnel");
    expect(within(funnel).getAllByRole("listitem").map((s) => s.getAttribute("data-step"))).toEqual(["1 visited", "2 signed up", "3 first message"]);
    expect(funnel).toHaveTextContent("20% of 1k users finished");
    expect(within(funnel).getAllByTestId("obs-funnel-dropoff")[0]).toHaveTextContent("25% continued · 750 dropped (75%)");
    expect(funnel).toHaveTextContent("the biggest drop is into 2 signed up");
  });
});
