// Copyright (c) 2025 Reliant Labs

/**
 * UPlotChart's two opt-in behaviours against a uPlot with real scale geometry:
 *
 *   area       `fill` tints every solid series; `stacked` plots running totals
 *              with a band between each pair, reads each series' own value in
 *              the cursor, and never stacks or fills a dashed comparison.
 *   exemplars  one button per sampled observation at its true (time, value) on
 *              the plot; one outside the axes is not drawn and not reported
 *              as placed, so the caller can show it elsewhere.
 *
 * Without either prop the chart is what it was.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { isolatedIndexes, timeExtent, placeExemplars, stackValues, UPlotChart, type ChartExemplar, type ChartSeries } from "../UPlotChart";

interface Built {
  opts: any;
  data: (number | null)[][];
}

const built: Built[] = [];

/** The plot area the fake reports, in canvas pixels (pxRatio 1: also CSS pixels). */
const PLOT = { left: 64, top: 8, width: 400, height: 200 };

vi.mock("uplot", () => {
  class FakeUPlot {
    static pxRatio = 1;
    /** uPlot's own UTC branch: the local fields of the result read as the UTC fields of the date. */
    static tzDate(date: Date, tz: string) {
      if (tz !== "UTC") throw new Error(`the fake only knows UTC, not ${tz}`);
      return new Date(+date + date.getTimezoneOffset() * 6e4);
    }
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
    /** CSS pixels from the plot's top-left, as uPlot reports them. */
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

const TIMES = [1000, 1060, 1120, 1180, 1240];

const series = (label: string, values: (number | null)[], extra: Partial<ChartSeries> = {}): ChartSeries => ({ label, values, ...extra });

const last = () => built[built.length - 1]!;

beforeEach(() => {
  built.length = 0;
});

describe("sparse time series", () => {
  it("bounds an October 2026 singleton independently of uPlot's poisoned callback max", () => {
    const time = Date.UTC(2026, 9, 25) / 1000;
    render(<UPlotChart timestamps={[time]} series={[series("DAU", [7])]} height={120} label="DAU" />);
    expect(last().opts.scales.x.range({}, time, time + 86_400_000)).toEqual([time - 30, time + 30]);
    expect(last().data).toEqual([[time], [7]]);
    expect(timeExtent([time, time + 86400])).toEqual([time, time + 86400]);
    expect(timeExtent([time, time + 60], [{ timeSec: time + 90, label: "release" }])).toEqual([time, time + 90]);
  });

  it("draws isolated glyphs without bridging nulls or adding history, in full charts and sparklines", () => {
    expect(isolatedIndexes([7, null, 9])).toEqual([0, 2]);
    expect(isolatedIndexes([null, 0, null, 1, 2, null, NaN])).toEqual([1]);
    for (const compact of [false, true]) {
      render(<UPlotChart timestamps={TIMES.slice(0, 3)} series={[series("RPCs", [7, null, 9])]} height={120} label="sparse" compact={compact} />);
      const opts = last().opts.series[1];
      expect(opts.points.filter({ data: last().data }, 1)).toEqual([0, 2]);
      expect(opts.spanGaps).toBe(false);
      expect(last().data[1]).toEqual([7, null, 9]);
    }
  });

  it("summarizes crowded annotations outside the data plot and keeps every accessible description", () => {
    const markers = TIMES.map((timeSec, i) => ({ timeSec, label: `release-${i}`, description: `production promoted release ${i}` }));
    const bands = TIMES.map((startSec, i) => ({ startSec, endSec: startSec + 60, label: `RPC errors ${i}` }));
    render(<UPlotChart timestamps={TIMES} series={[series("RPCs", [7, 8, 9, 8, 7])]} height={120} label="crowded" markers={markers} bands={bands} />);
    const summary = screen.getByTestId("chart-annotations");
    expect(summary).toHaveClass("truncate");
    expect(summary).toHaveTextContent("5 releases · 5 alerts:");
    const caption = screen.getByRole("group", { name: "crowded" }).querySelector("figcaption")!;
    for (let i = 0; i < 5; i++) {
      expect(caption).toHaveTextContent(`production promoted release ${i}`);
      expect(caption).toHaveTextContent(`RPC errors ${i}`);
    }
  });
});

describe("stackValues", () => {
  it("accumulates the solid series in order, leaves a gap as a gap, and passes a dashed series through", () => {
    const stacked = stackValues([
      series("a", [1, 2, null, 4]),
      series("b", [10, 20, 30, null]),
      series("prev", [7, 7, 7, 7], { dashed: true }),
      series("c", [100, 100, 100, 100]),
    ]);
    expect(stacked[0]).toEqual([1, 2, null, 4]);
    expect(stacked[1]).toEqual([11, 22, 30, null]);
    expect(stacked[2]).toEqual([7, 7, 7, 7]);
    // The gap in `a` contributes nothing to the total, and `b`'s gap leaves c alone at its own height.
    expect(stacked[3]).toEqual([111, 122, 130, 104]);
  });
});

describe("area", () => {
  const two = [series("a", [1, 2, 3, 2, 1]), series("b", [2, 2, 2, 2, 2])];

  it("is today's chart when not asked for: a fill under a lone series, none under several", () => {
    render(<UPlotChart timestamps={TIMES} series={[two[0]!]} height={120} label="one" />);
    expect(last().opts.series[1].fill).toBeDefined();
    render(<UPlotChart timestamps={TIMES} series={two} height={120} label="two" />);
    expect(last().opts.series[1].fill).toBeUndefined();
    expect(last().opts.series[2].fill).toBeUndefined();
    expect(last().opts.bands).toBeUndefined();
  });

  it("fill tints every solid series, not a dashed one, and starts the axis at zero", () => {
    render(<UPlotChart timestamps={TIMES} series={[...two, series("prev", [1, 1, 1, 1, 1], { dashed: true })]} height={120} label="fill" area="fill" />);
    const [, a, b, prev] = last().opts.series;
    expect(a.fill).toBeDefined();
    expect(b.fill).toBeDefined();
    expect(prev.fill).toBeUndefined();
    expect(last().opts.bands).toBeUndefined();
    expect(last().data[1]).toEqual([1, 2, 3, 2, 1]);
    expect(last().opts.scales.y.range({}, 2, 10)[0]).toBe(0);
  });

  it("stacked plots running totals, fills each band between neighbours, and reads each series' own value", () => {
    render(<UPlotChart timestamps={TIMES} series={two} height={120} label="stack" area="stacked" unit="count" />);
    expect(last().data[1]).toEqual([1, 2, 3, 2, 1]);
    expect(last().data[2]).toEqual([3, 4, 5, 4, 3]);
    // One band: series 2 (b) over series 1 (a); a fills to the baseline itself.
    expect(last().opts.bands).toHaveLength(1);
    expect(last().opts.bands[0].series).toEqual([2, 1]);
    expect(last().opts.series[1].fill).toBeDefined();
    expect(last().opts.series[2].fill).toBeUndefined();
    // The cursor shows b's own 2, not the total 3 or 5.
    expect(last().opts.series[2].value({}, 5, 2, 0)).toBe("2");
    expect(last().opts.series[2].value({}, 3, 2, null)).toBe("—");
  });

  it("describes each series' own figures in the caption, not the running total", () => {
    render(<UPlotChart timestamps={TIMES} series={two} height={120} label="stack" area="stacked" unit="count" />);
    const caption = screen.getByRole("group", { name: "stack" }).querySelector("figcaption")!;
    expect(caption).toHaveTextContent("b: latest 2, between 2 and 2.");
    expect(caption).not.toHaveTextContent("latest 3");
  });

  it("never stacks a sparkline", () => {
    render(<UPlotChart timestamps={TIMES} series={two} height={40} label="spark" area="stacked" compact />);
    expect(last().opts.bands).toBeUndefined();
    expect(last().data[2]).toEqual([2, 2, 2, 2, 2]);
  });
});

describe("placeExemplars", () => {
  const u = {
    scales: { x: { min: 1000, max: 1240 }, y: { min: 0, max: 10 } },
    valToPos: (value: number, scale: string) => {
      const s = scale === "x" ? { min: 1000, max: 1240, size: 400 } : { min: 0, max: 10, size: 200 };
      const unit = (value - s.min) / (s.max - s.min);
      return scale === "x" ? unit * s.size : (1 - unit) * s.size;
    },
  } as unknown as Parameters<typeof placeExemplars>[0];
  const ex = (timeSec: number, value: number): ChartExemplar => ({ timeSec, value, label: `${timeSec}/${value}` });

  it("puts each exemplar at its time and value, and drops one outside either axis", () => {
    const placed = placeExemplars(u, [ex(1000, 0), ex(1120, 5), ex(1240, 10), ex(1120, 11), ex(999, 5), ex(1241, 5), ex(1120, -1), ex(1120, Number.NaN)]);
    expect(placed).toEqual([
      { index: 0, x: 0, y: 200 },
      { index: 1, x: 200, y: 100 },
      { index: 2, x: 400, y: 0 },
    ]);
  });

  it("places nothing before the chart has scales", () => {
    expect(placeExemplars({ scales: {}, valToPos: () => 0 } as unknown as Parameters<typeof placeExemplars>[0], [ex(1, 1)])).toEqual([]);
  });
});

describe("exemplars on the chart", () => {
  // y axis: series peak at 3, so with an area it is [0, 3.3]; without, [1, 3]. Use no area: [1, 3].
  const line = [series("p95", [1, 2, 3, 2, 1])];

  it("renders a button at each exemplar's true position inside the plot, named and styled by whether it has a trace", () => {
    const placed = vi.fn();
    render(
      <UPlotChart
        timestamps={TIMES}
        series={line}
        height={220}
        label="latency"
        exemplars={[
          { timeSec: 1120, value: 2, solid: true, label: "stored at 2" },
          { timeSec: 1240, value: 3, label: "sampled out at 3" },
        ]}
        onExemplarsPlaced={placed}
      />
    );
    const layer = screen.getByTestId("chart-exemplar-layer");
    // The layer is the plot rectangle, so a dot's left/top are plot-relative.
    expect(layer).toHaveStyle({ left: `${PLOT.left}px`, top: `${PLOT.top}px`, width: `${PLOT.width}px`, height: `${PLOT.height}px` });
    const [stored, sampled] = screen.getAllByTestId("chart-exemplar-dot");
    // x axis is 1000..1240: 1120 is the middle; y axis is 1..3: 2 is the middle, 3 the top.
    expect(stored).toHaveStyle({ left: "200px", top: "100px" });
    expect(stored).toHaveAccessibleName("stored at 2");
    expect(stored).toHaveAttribute("data-solid");
    expect(sampled).toHaveStyle({ left: "400px", top: "0px" });
    expect(sampled).not.toHaveAttribute("data-solid");
    expect(placed).toHaveBeenLastCalledWith([0, 1]);
  });

  it("leaves out an exemplar above the axis, and says which were placed so the caller can show the rest", () => {
    const placed = vi.fn();
    render(
      <UPlotChart
        timestamps={TIMES}
        series={line}
        height={220}
        label="latency"
        exemplars={[
          { timeSec: 1060, value: 2.5, label: "inside" },
          { timeSec: 1060, value: 40, label: "far above the axis" },
          { timeSec: 5000, value: 2, label: "long after the window" },
        ]}
        onExemplarsPlaced={placed}
      />
    );
    expect(screen.getAllByTestId("chart-exemplar-dot")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "inside" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "far above the axis" })).not.toBeInTheDocument();
    expect(placed).toHaveBeenLastCalledWith([0]);
  });

  it("does not stretch the axis to reach an exemplar", () => {
    render(<UPlotChart timestamps={TIMES} series={line} height={220} label="latency" exemplars={[{ timeSec: 1060, value: 500, label: "huge" }]} />);
    expect(last().opts.scales.y).toBeUndefined();
    expect(screen.queryByTestId("chart-exemplar-dot")).not.toBeInTheDocument();
  });

  it("opens the exemplar that was clicked, with the dot's position for a popover", async () => {
    const user = userEvent.setup();
    const open = vi.fn();
    render(
      <UPlotChart
        timestamps={TIMES}
        series={line}
        height={220}
        label="latency"
        exemplars={[
          { timeSec: 1060, value: 2, label: "first" },
          { timeSec: 1180, value: 2, label: "second" },
        ]}
        onExemplarOpen={open}
      />
    );
    await user.click(screen.getByRole("button", { name: "second" }));
    expect(open).toHaveBeenCalledTimes(1);
    expect(open.mock.calls[0]![0]).toBe(1);
    expect(open.mock.calls[0]![1]).toEqual({ x: expect.any(Number), y: expect.any(Number) });
  });

  it("draws no layer, and no extra hooks, for a chart that passes none", () => {
    render(<UPlotChart timestamps={TIMES} series={line} height={220} label="plain" />);
    expect(screen.queryByTestId("chart-exemplar-layer")).not.toBeInTheDocument();
    expect(last().opts.hooks).toBeUndefined();
  });

  it("places dots on an area chart against the zero-based axis", () => {
    render(<UPlotChart timestamps={TIMES} series={line} height={220} label="latency" area="fill" exemplars={[{ timeSec: 1120, value: 1.65, label: "mid" }]} />);
    // area y axis is [0, 3.3]: 1.65 is exactly the middle.
    expect(screen.getByTestId("chart-exemplar-dot")).toHaveStyle({ top: "100px" });
  });
});

describe("a day bucket in a short window (the empty-dashboards audit's workflow panel)", () => {
  // 2026-10-29 00:00Z is the Date bucket; the dashboard opened on the last hour, 18:11Z to 19:11Z.
  const day = Date.UTC(2026, 9, 29) / 1000;
  const hour = { startSec: Date.UTC(2026, 9, 29, 18, 11, 39) / 1000, endSec: Date.UTC(2026, 9, 29, 19, 11, 39) / 1000 };
  const week = { startSec: Date.UTC(2026, 9, 22, 19, 11, 39) / 1000, endSec: Date.UTC(2026, 9, 29, 19, 11, 39) / 1000 };

  it("spans the window that was asked for, not a viewport around the one bucket", () => {
    // Without a window the axis is the one-minute box the audit's screenshot shows.
    expect(timeExtent([day])).toEqual([day - 30, day + 30]);
    // With it, the axis is the window, extended back to the bucket's start (a day begins before the hour does).
    expect(timeExtent([day], [], hour)).toEqual([day, hour.endSec]);
    // Seven days of buckets sit inside the seven-day window; the window sets the axis ends.
    const days = Array.from({ length: 8 }, (_, i) => day - (7 - i) * 86_400);
    expect(timeExtent(days, [], week)).toEqual([days[0], week.endSec]);
    expect(timeExtent([], [], hour)).toEqual([hour.startSec, hour.endSec]);
  });

  it("hands uPlot the window as the x range", () => {
    render(<UPlotChart timestamps={[day]} series={[series("success", [53])]} height={120} label="runs" window={hour} />);
    expect(last().opts.scales.x.range({}, day, day)).toEqual([day, hour.endSec]);
  });

  it("marks a series with a single point, larger than an isolated glyph, and still marks nothing across gaps", () => {
    render(<UPlotChart timestamps={[day]} series={[series("success", [53]), series("failed", [null])]} height={120} label="runs" window={hour} />);
    const [success] = last().opts.series.slice(1);
    expect(success.points.filter({ data: last().data }, 1)).toEqual([0]);
    expect(success.points.size).toBe(9);
    expect(success.points.fill).toBe(success.stroke);

    render(<UPlotChart timestamps={TIMES.slice(0, 3)} series={[series("many", [7, 8, 9])]} height={120} label="runs" />);
    expect(last().opts.series[1].points.size).toBe(6);
    expect(last().opts.series[1].points.filter({ data: last().data }, 1)).toEqual([]);
  });

  it.each(["America/Los_Angeles", "Pacific/Auckland", "UTC"])("labels a UTC calendar-day bucket as that date when the browser is in %s", (zone) => {
    const was = process.env.TZ;
    process.env.TZ = zone;
    try {
      calendarDayLabel();
    } finally {
      if (was === undefined) delete process.env.TZ;
      else process.env.TZ = was;
    }
  });

  function calendarDayLabel() {
    render(<UPlotChart timestamps={[day]} series={[series("success", [53])]} height={120} label="runs" calendar={{ timezone: "UTC", bucketSeconds: 86_400 }} />);
    const { opts } = last();
    // uPlot reads local fields of what tzDate returns: for 00:00Z the UTC date must be 29 October in every zone.
    const shifted = opts.tzDate(day) as Date;
    expect([shifted.getFullYear(), shifted.getMonth(), shifted.getDate(), shifted.getHours()]).toEqual([2026, 9, 29, 0]);
    expect(opts.series[0].value).toBe("{YYYY}-{MM}-{DD}");
    expect(screen.getByTestId("chart-bucket-note")).toHaveTextContent("1-day buckets: UTC calendar dates");
    expect(screen.getByRole("group", { name: "runs" }).querySelector("figcaption")).toHaveTextContent("1-day buckets: UTC calendar dates.");
  }

  it("leaves instants on the browser's zone and draws no bucket note", () => {
    render(<UPlotChart timestamps={TIMES.slice(0, 3)} series={[series("many", [7, 8, 9])]} height={120} label="plain" />);
    expect(last().opts.tzDate).toBeUndefined();
    expect(screen.queryByTestId("chart-bucket-note")).toBeNull();
  });
});
