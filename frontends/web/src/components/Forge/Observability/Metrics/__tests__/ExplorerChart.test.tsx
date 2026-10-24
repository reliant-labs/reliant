// Copyright (c) 2025 Reliant Labs

/**
 * What the explorer asks UPlotChart for:
 *
 *   area       stacked when every drawn series is an additive aggregation in one
 *              unit (rates of two counters), a translucent overlap otherwise (a
 *              percentile does not add), and the overlap, not the stack, when a
 *              comparison is on.
 *   exemplars  dots on the chart at their value; the strip under it only for
 *              what the axis does not reach, and for all of them on a stack
 *              whose axis is a total.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { MetricExemplar } from "@/services/controlPlane/observability/metricsTypes";
import type { Panel } from "@/services/controlPlane/observability/types";

import { fixtureMetricPanel } from "../../__fixtures__/metricFixtures";
import { ExplorerChart } from "../ExplorerChart";
import { areaModeFor } from "../seriesView";

interface Built {
  opts: any;
  data: (number | null)[][];
}
const built: Built[] = [];

vi.mock("uplot", () => {
  const PLOT = { left: 64, top: 8, width: 400, height: 200 };
  class FakeUPlot {
    static pxRatio = 1;
    width = 0;
    bbox = PLOT;
    scales: Record<string, { min: number; max: number }>;
    constructor(opts: Built["opts"], data: Built["data"]) {
      built.push({ opts, data });
      const xs = data[0] as number[];
      const ys = data.slice(1).flat().filter((v): v is number => v !== null);
      const [xMin, xMax] = opts.scales.x.range(this, Math.min(...xs), Math.max(...xs)) as [number, number];
      const yRange = opts.scales.y?.range;
      const [yMin, yMax] = yRange ? (yRange(this, Math.min(...ys), Math.max(...ys)) as [number, number]) : [Math.min(...ys), Math.max(...ys)];
      this.scales = { x: { min: xMin, max: xMax }, y: { min: yMin, max: yMax } };
    }
    valToPos(value: number, scale: "x" | "y") {
      const { min, max } = this.scales[scale]!;
      const unit = (value - min) / (max - min);
      return scale === "x" ? unit * PLOT.width : (1 - unit) * PLOT.height;
    }
    destroy() {}
    setSize() {}
  }
  return { default: FakeUPlot };
});

const NOW = Date.UTC(2026, 9, 24, 12);
const WINDOW = { startMs: NOW - 30 * 60_000, endMs: NOW };

const rate = (label: string, field: string) => ({ label, agg: "rate", field, metricType: "sum" });
const p95 = (label: string, field: string) => ({ label, agg: "quantile", quantile: 0.95, field, metricType: "histogram" });

function panelOf(series: Panel["series"]): Panel {
  return { id: "explore", title: "Explore", kind: "line", signal: "metrics", series };
}

const ready = (panel: Panel) => ({ data: fixtureMetricPanel(panel, WINDOW, NOW), isLoading: false, error: null });
const last = () => built[built.length - 1]!;

beforeEach(() => {
  built.length = 0;
});

describe("areaModeFor", () => {
  const meta = (label: string, agg: string, valueUnit = "{call}/s", metric = "m") => ({ label, agg, valueUnit, metric, kind: "sum" as const });

  it("stacks additive aggregations that share a unit", () => {
    expect(areaModeFor([meta("A", "rate"), meta("B", "increase", "{call}/s"), meta("C", "sum", "{call}/s")], ["A", "B", "C"])).toBe("stacked");
  });

  it("overlaps what does not add: a percentile, an average, a formula, mixed units, a lone series", () => {
    expect(areaModeFor([meta("A", "quantile"), meta("B", "quantile")], ["A", "B"])).toBe("fill");
    expect(areaModeFor([meta("A", "rate"), meta("B", "avg")], ["A", "B"])).toBe("fill");
    expect(areaModeFor([meta("A", "rate"), meta("F", "rate", "{call}/s", "")], ["A", "F"])).toBe("fill");
    expect(areaModeFor([meta("A", "rate", "{call}/s"), meta("B", "rate", "By/s")], ["A", "B"])).toBe("fill");
    expect(areaModeFor([meta("A", "rate")], ["A"])).toBe("fill");
    expect(areaModeFor([meta("A", "rate")], ["A", "unknown"])).toBe("fill");
  });
});

describe("the area chart", () => {
  const two = panelOf([rate("A", "rpc.server.calls"), rate("B", "rpc.server.calls")]);

  it("stacks two rates: running totals, one band, and the newest open bucket stacked with the rest and named", () => {
    const query = ready(two);
    render(<ExplorerChart panel={two} query={query} unit="rate" hidden={[]} attributes={[]} chart="area" />);
    const [a, b] = query.data.series;
    expect(last().data[1]).toEqual(a!.values);
    expect(last().data[2]).toEqual(b!.values.map((v, i) => (v ?? 0) + (a!.values[i] ?? 0)));
    expect(last().opts.bands).toHaveLength(1);
    // Two series, no "(still filling)" line: an open bucket is part of the stack.
    expect(last().opts.series).toHaveLength(3);
    expect(screen.getByTestId("metrics-chart")).toHaveTextContent("The newest bucket is still filling.");
  });

  it("overlaps two percentiles instead of stacking them", () => {
    const latencies = panelOf([p95("A", "checkout.latency"), p95("B", "rpc.server.duration")]);
    render(<ExplorerChart panel={latencies} query={ready(latencies)} unit="s" hidden={[]} attributes={[]} chart="area" />);
    expect(last().opts.bands).toBeUndefined();
    // A and B each have a dashed "still filling" tail between them; both lines fill.
    const solid = last().opts.series.filter((s: { label?: string; dash?: number[] }) => s.label && !s.dash);
    expect(solid.map((s: { label: string }) => s.label)).toEqual(["A", "B"]);
    expect(solid.every((s: { fill?: string }) => s.fill !== undefined)).toBe(true);
  });

  it("overlaps rather than stacks when a comparison is on, so the dashed line sits at the value it compares", () => {
    const query = ready(two);
    const compare = { query: ready(two), label: "previous period", offsetSec: 1800 };
    render(<ExplorerChart panel={two} query={query} compare={compare} unit="rate" hidden={[]} attributes={[]} chart="area" />);
    expect(last().opts.bands).toBeUndefined();
    const dashed = last().opts.series.filter((s: { dash?: number[] }) => s.dash);
    expect(dashed.length).toBeGreaterThan(0);
    expect(dashed.every((s: { fill?: string }) => s.fill === undefined)).toBe(true);
  });

  it("is a plain line chart when the chart kind is line", () => {
    render(<ExplorerChart panel={two} query={ready(two)} unit="rate" hidden={[]} attributes={[]} chart="line" />);
    expect(last().opts.bands).toBeUndefined();
    expect(last().opts.scales.y).toBeUndefined();
  });
});

describe("exemplars", () => {
  const latency = panelOf([p95("A", "checkout.latency")]);
  const exemplar = (value: number, traceStored: boolean, minute: number, id: string): MetricExemplar => ({
    timeMs: WINDOW.startMs + minute * 60_000,
    value,
    traceId: id.padEnd(32, "0"),
    spanId: id.padEnd(16, "0"),
    attributes: {},
    traceStored,
  });

  function overlay(items: MetricExemplar[], onOpen = vi.fn()) {
    return { items, stepSeconds: 60, onOpen };
  }

  it("draws a dot on the chart at the exemplar's value, and puts only what the axis does not reach on the strip", () => {
    const query = ready(latency);
    const values = query.data.series[0]!.values.filter((v): v is number => v !== null);
    const inside = (Math.min(...values) + Math.max(...values)) / 2;
    const items = [exemplar(inside, true, 10, "a1"), exemplar(Math.max(...values) * 50, false, 12, "b2")];
    render(<ExplorerChart panel={latency} query={query} unit="s" hidden={[]} attributes={[]} chart="line" exemplars={overlay(items)} />);

    const onChart = screen.getAllByTestId("chart-exemplar-dot");
    expect(onChart).toHaveLength(1);
    expect(onChart[0]).toHaveAttribute("data-solid");
    // Halfway up the axis and a third of the way along the window: where the value and the time say.
    expect(onChart[0]).toHaveStyle({ top: "100px" });

    const strip = screen.getByTestId("metrics-exemplar-rail");
    expect(strip).toHaveTextContent("outside the chart's axis");
    const stripped = within(strip).getAllByTestId("metrics-exemplar-dot");
    expect(stripped).toHaveLength(1);
    expect(stripped[0]).not.toHaveAttribute("data-stored");
  });

  it("draws no strip when every exemplar is on the chart", () => {
    const query = ready(latency);
    const values = query.data.series[0]!.values.filter((v): v is number => v !== null);
    const mid = (Math.min(...values) + Math.max(...values)) / 2;
    render(<ExplorerChart panel={latency} query={query} unit="s" hidden={[]} attributes={[]} chart="line" exemplars={overlay([exemplar(mid, true, 5, "c3")])} />);
    expect(screen.getAllByTestId("chart-exemplar-dot")).toHaveLength(1);
    expect(screen.queryByTestId("metrics-exemplar-rail")).not.toBeInTheDocument();
  });

  it("opens the exemplar behind the dot that was clicked", async () => {
    const user = userEvent.setup();
    const onOpen = vi.fn();
    const query = ready(latency);
    const values = query.data.series[0]!.values.filter((v): v is number => v !== null);
    const mid = (Math.min(...values) + Math.max(...values)) / 2;
    const items = [exemplar(mid, true, 5, "d4"), exemplar(mid, false, 15, "e5")];
    render(<ExplorerChart panel={latency} query={query} unit="s" hidden={[]} attributes={[]} chart="line" exemplars={overlay(items, onOpen)} />);
    await user.click(screen.getAllByTestId("chart-exemplar-dot")[1]!);
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(onOpen.mock.calls[0]![0]).toBe(items[1]);
  });

  it("puts every exemplar on the strip when the chart is a stack, whose axis is a total", () => {
    const stack = panelOf([rate("A", "rpc.server.calls"), rate("B", "rpc.server.calls")]);
    const items = [exemplar(2, true, 5, "f6"), exemplar(3, false, 9, "g7")];
    render(<ExplorerChart panel={stack} query={ready(stack)} unit="rate" hidden={[]} attributes={[]} chart="area" exemplars={overlay(items)} />);
    expect(screen.queryByTestId("chart-exemplar-dot")).not.toBeInTheDocument();
    expect(within(screen.getByTestId("metrics-exemplar-rail")).getAllByTestId("metrics-exemplar-dot")).toHaveLength(2);
  });
});
