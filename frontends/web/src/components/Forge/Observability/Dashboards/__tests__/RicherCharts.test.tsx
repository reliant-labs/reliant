// Copyright (c) 2025 Reliant Labs

/**
 * The presentation forge's spec added for richer dashboards (dashspec):
 * a sql panel drawn as a heatmap, a sql figure's sparkline from its trendSql,
 * palette colours by series name, section groups and a default comparison.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";

import type { Dashboard, Panel, PanelResult } from "@/services/controlPlane/observability/types";
import { UPlotChart } from "../../charts/UPlotChart";
import { PALETTE_KEY_TOKENS, paletteToken } from "../../charts/chartPalette";
import { dashboardProblems, panelProblems } from "../../obsdashSpec";
import { dashboardDefaultCompare } from "../dashboardModel";
import { PanelBody, sqlFigures } from "../PanelBody";
import { pieSlices } from "../CategoryChart";
import { CHART_PALETTE, dealColors, namedToken, seriesEntries, alignTo, unionTimestamps } from "../seriesModel";
import { cellStrength, sqlMatrix } from "../SqlHeatmap";
import { drawsSqlFigures, panelGroups, sqlTrendPanel } from "../viewerModel";

vi.mock("../../charts/UPlotChart", () => ({ UPlotChart: vi.fn(() => <div data-testid="sql-chart" />) }));

const sql = (extra: Partial<Panel> = {}): Panel => ({ id: "p", title: "P", kind: "sql", signal: "", series: [], sql: "SELECT 1", ...extra });
const result = (data: Partial<PanelResult> = {}): PanelResult => ({
  kind: "sql", unit: "", series: [], columns: [], rows: [], stepSeconds: 0,
  truncated: false, funnelSteps: [], issues: [], sessions: [], seriesMeta: [],
  seriesTotal: 0, warnings: [], compiledSql: [], ...data,
});
const ready = (data: PanelResult) => ({ data, isLoading: false, error: null });
const row = (group: Record<string, string>, values: (number | null)[]) => ({ group, values });
const line = (label: string, values: (number | null)[], group: Record<string, string> = {}) => ({ label, group, timestamps: values.map((_, i) => 1_700_000_000 + i * 86_400), values, value: null });

describe("palette keys", () => {
  it("name the chart tokens, and danger the error token; an unknown key names none", () => {
    expect(paletteToken("blue")).toBe("--chart-1");
    expect(paletteToken("slate")).toBe("--chart-6");
    expect(paletteToken("danger")).toBe("--destructive");
    expect(paletteToken("green")).toBeUndefined();
    expect(paletteToken("#ff0000")).toBeUndefined();
    expect(paletteToken("toString")).toBeUndefined();
    expect(Object.keys(PALETTE_KEY_TOKENS)).toEqual(["blue", "violet", "cyan", "amber", "fuchsia", "slate", "danger"]);
  });

  it("give a named series its colour, and deal the rest the colours nobody named", () => {
    const items = ["Signed in", "Anonymous", "Bots", "errors"];
    const named = (label: string) => namedToken({ colors: { Anonymous: "blue", "Signed in": "violet" } }, [label]);
    const dealt = dealColors(items, (label) => label === "errors", named).map((c) => c.token);
    expect(dealt).toEqual(["--chart-2", "--chart-1", "--chart-3", "--destructive"]);
    // Nothing named: the palette in rank order, as before.
    expect(dealColors(["a", "b"], () => false).map((c) => c.token)).toEqual([CHART_PALETTE[0], CHART_PALETTE[1]]);
  });

  it("let an explicit colour win over the error rule, and the panel's colour paint a chart of one series", () => {
    const panel = sql({ display: { colors: { failed: "amber" } } });
    const entries = seriesEntries(panel, { series: [line("failed", [1, 2])] }, [], unionTimestamps([line("failed", [1, 2])]), alignTo, "last");
    expect(entries[0].token).toBe("--chart-4");
    const one = seriesEntries(sql({ display: { color: "fuchsia" } }), { series: [line("events", [1])] }, [], [1_700_000_000], alignTo, "last");
    expect(one[0].token).toBe("--chart-5");
    const two = seriesEntries(sql({ display: { color: "fuchsia" } }), { series: [line("a", [2]), line("b", [1])] }, [], [1_700_000_000], alignTo, "last");
    expect(two.map((e) => e.token)).toEqual(["--chart-1", "--chart-2"]);
  });

  it("colour a group's series by its value, and a pie's slices by name", () => {
    const panel = sql({ display: { colors: { invoice: "amber" } } });
    const series = [line("records", [5], { entity: "order" }), line("records", [3], { entity: "invoice" })];
    const entries = seriesEntries(panel, { series }, ["entity"], [1_700_000_000], alignTo, "last");
    expect(entries.map((e) => e.token)).toEqual(["--chart-1", "--chart-4"]);
    const slices = pieSlices({ display: { style: "pie", colors: { "/cart": "slate" } } }, { rows: [row({ route: "/" }, [9]), row({ route: "/cart" }, [4])] }, ["route"]);
    expect(slices.map((s) => s.token)).toEqual(["--chart-1", "--chart-6"]);
  });
});

describe("a sql panel drawn as a heatmap", () => {
  const retention = result({
    columns: ["W1", "W2"],
    rows: [row({ "Cohort (week of)": "2026-10-05 · 40 users" }, [0.5, 0.25]), row({ "Cohort (week of)": "2026-10-12 · 32 users" }, [0.4, null])],
  });

  it("reads a wide result as rows of cells, a numeric column each, a NULL as no value", () => {
    expect(sqlMatrix(sql(), retention, ["Cohort (week of)"])).toEqual({
      rows: ["2026-10-05 · 40 users", "2026-10-12 · 32 users"],
      columns: ["W1", "W2"],
      cells: [[0.5, 0.25], [0.4, null]],
      min: 0.25,
      max: 0.5,
    });
  });

  it("reads a long result (two text columns, one number) as rows by the first and columns by the second", () => {
    const long = result({ columns: ["share"], rows: [row({ cohort: "a", week: "W1" }, [0.5]), row({ cohort: "a", week: "W2" }, [0.2]), row({ cohort: "b", week: "W1" }, [0.4])] });
    const grid = sqlMatrix(sql(), long, ["cohort", "week"]);
    expect(grid.rows).toEqual(["a", "b"]);
    expect(grid.columns).toEqual(["W1", "W2"]);
    expect(grid.cells).toEqual([[0.5, 0.2], [0.4, null]]);
  });

  it("reads a time series as a row per series and a column per bucket", () => {
    const grid = sqlMatrix(sql(), result({ series: [line("records", [1, 2], { entity: "order" }), line("records", [3, 4], { entity: "invoice" })], bucketSeconds: 86_400, bucketTimezone: "UTC" }), ["entity"]);
    expect(grid.rows).toEqual(["order — records", "invoice — records"]);
    expect(grid.columns).toHaveLength(2);
    expect(grid.cells).toEqual([[1, 2], [3, 4]]);
  });

  it("shades from the smallest value to the largest, so a narrow range keeps its contrast", () => {
    expect(cellStrength(0.02, 0.02, 0.04)).toBeCloseTo(0.08);
    expect(cellStrength(0.04, 0.02, 0.04)).toBeCloseTo(0.72);
    expect(cellStrength(5, 5, 5)).toBe(0.4);
  });

  it("draws a table in the panel's colour, the unit on every cell, and an empty cell for a week to come", () => {
    render(<PanelBody panel={sql({ unit: "percent", display: { style: "heatmap", color: "violet" } })} query={ready(retention)} />);
    const table = within(screen.getByTestId("obs-panel-sql-heatmap")).getByRole("table");
    const cells = within(table).getAllByRole("cell");
    expect(cells.map((c) => c.textContent)).toEqual(["50%", "25%", "40%", "·"]);
    expect(cells[0].getAttribute("style")).toContain("--chart-2");
    expect(cells[3]).not.toHaveAttribute("data-value");
    expect(screen.queryByTestId("obs-panel-table")).not.toBeInTheDocument();
  });
});

describe("a sql figure's sparkline and comparison", () => {
  const figure = result({ columns: ["Daily active users"], rows: [row({}, [42])] });

  it("takes its trend from the trendSql series named like the figure, or the only one", () => {
    const named = sqlFigures(figure, result({ series: [line("Daily active users", [3, 4])] }));
    expect(named.series[0]).toMatchObject({ label: "Daily active users", value: 42, values: [3, 4] });
    const only = sqlFigures(figure, result({ series: [line("day users", [5, 6])] }));
    expect(only.series[0].values).toEqual([5, 6]);
    const grouped = sqlFigures(figure, result({ series: [line("x", [1, 2], { env: "prod" })] }));
    expect(grouped.series[0].values).toEqual([]);
  });

  it("draws the sparkline in the panel's colour, beside the change against the comparison", () => {
    vi.mocked(UPlotChart).mockClear();
    render(
      <PanelBody
        panel={sql({ display: { color: "cyan" } })}
        query={ready(figure)}
        trend={ready(result({ series: [line("Daily active users", [30, 35, 42])] }))}
        compare={{ query: ready(result({ columns: ["Daily active users"], rows: [row({}, [36])] })), label: "previous period", offsetSec: 86_400 }}
      />
    );
    expect(screen.getByTestId("obs-panel-number")).toHaveTextContent("42");
    expect(screen.getByTestId("obs-panel-change")).toHaveTextContent("vs previous period");
    expect(screen.getByTestId("obs-panel-sparkline")).toBeInTheDocument();
    const props = vi.mocked(UPlotChart).mock.calls.at(-1)![0];
    expect(props.series[0]).toMatchObject({ color: "--chart-3", values: [30, 35, 42] });
  });

  it("asks for a trend and a comparison only where the panel draws figures", () => {
    expect(drawsSqlFigures(sql(), figure)).toBe(true);
    expect(drawsSqlFigures(sql({ display: { style: "heatmap" } }), figure)).toBe(false);
    expect(drawsSqlFigures(sql(), result({ columns: ["n"], rows: [row({}, [1]), row({}, [2])] }))).toBe(false);
    expect(drawsSqlFigures(sql(), undefined)).toBe(false);
    expect(sqlTrendPanel(sql({ trendSql: "SELECT 2" }))).toMatchObject({ id: "p.trend", sql: "SELECT 2", trendSql: undefined, display: undefined });
    expect(sqlTrendPanel(sql())).toBeUndefined();
    expect(sqlTrendPanel(sql({ trendSql: "SELECT 2", display: { style: "funnel" } }))).toBeUndefined();
  });
});

describe("sections and the default comparison", () => {
  const panel = (id: string, section?: string): Panel => ({ ...sql(), id, section });
  const dashboard: Pick<Dashboard, "sections" | "panels"> = {
    sections: [{ id: "a", title: "A" }, { id: "b", title: "B", description: "Bee" }, { id: "empty", title: "Nothing" }],
    panels: [panel("b1", "b"), panel("loose"), panel("a1", "a"), panel("b2", "b"), panel("stray", "undeclared")],
  };

  it("draws the unsectioned panels first, then each section in its declared order, empty ones not at all", () => {
    const groups = panelGroups(dashboard, {});
    expect(groups.map((g) => [g.section?.id, g.panels.map((p) => p.id)])).toEqual([
      [undefined, ["loose", "stray"]],
      ["a", ["a1"]],
      ["b", ["b1", "b2"]],
    ]);
  });

  it("keeps a hidden section's heading with no panels, and drops a hidden panel", () => {
    const groups = panelGroups(dashboard, { hiddenSections: ["b"], hiddenPanels: ["a1"] });
    expect(groups.map((g) => [g.section?.id, g.hidden, g.panels.map((p) => p.id)])).toEqual([
      [undefined, false, ["loose", "stray"]],
      ["a", false, []],
      ["b", true, []],
    ]);
  });

  it("opens with the dashboard's own comparison, and off for none or one this console does not know", () => {
    expect(dashboardDefaultCompare("period")).toBe("period");
    expect(dashboardDefaultCompare("release")).toBe("release");
    expect(dashboardDefaultCompare(undefined)).toBe("off");
    expect(dashboardDefaultCompare("week")).toBe("off");
  });
});

describe("the editor's copy of forge's rules", () => {
  it("accepts a sql heatmap, a trend, palette colours and a default comparison", () => {
    expect(panelProblems(sql({ display: { style: "heatmap", color: "violet" } }))).toEqual([]);
    expect(panelProblems(sql({ trendSql: "SELECT 2", display: { colors: { a: "blue", b: "danger" } } }))).toEqual([]);
    expect(dashboardProblems({ id: "d", title: "D", filters: [], defaultCompare: "period", sections: [{ id: "s", title: "S", description: "x" }], panels: [sql()] })).toEqual([]);
  });

  it("refuses a raw colour, a per-series colour on a heatmap, a trend on a funnel, a trend elsewhere, and an unknown comparison", () => {
    expect(panelProblems(sql({ display: { color: "#ff0000" } }))).toEqual([expect.stringContaining('"#ff0000" is not a palette colour')]);
    expect(panelProblems(sql({ display: { style: "heatmap", colors: { W1: "blue" } } }))).toEqual([expect.stringContaining("drawn in one colour")]);
    expect(panelProblems(sql({ trendSql: "SELECT 2", display: { style: "funnel" } }))).toEqual([expect.stringContaining("takes no sparkline")]);
    expect(panelProblems({ id: "l", title: "L", kind: "line", signal: "traces", series: [{ label: "n", agg: "count" }], trendSql: "SELECT 1" })).toEqual([
      "Only a sql panel takes a sparkline statement.",
    ]);
    expect(dashboardProblems({ id: "d", title: "D", filters: [], defaultCompare: "week", panels: [sql()] })).toEqual([expect.stringContaining('"week" is not a comparison')]);
  });
});
