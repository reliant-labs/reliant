// Copyright (c) 2026 Reliant Labs

/**
 * A heatmap reads the time bucket (column) under the pointer like every
 * chart, the compact one on a card included: a tooltip lists each value
 * bucket's count there, highest first, and the keys step through the columns
 * that have events.
 */

import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import type { HeatmapCellResult } from "@/services/controlPlane/observability/types";

import { formatSampleTime } from "../../charts/chartHover";
import { HeatmapChart, heatmapColumnRows, heatmapGrid } from "../HeatmapChart";

const cell = (timeSec: number, low: number, high: number, count: number) => ({ timeSec, low, high, count }) as HeatmapCellResult;

// Three one-minute columns over 0–180s; the middle one is empty.
const CELLS = [cell(0, 10, 20, 4), cell(0, 100, 200, 1), cell(120, 10, 20, 7)];
const WINDOW = { startMs: 0, endMs: 180_000 };

describe("heatmapColumnRows", () => {
  const grid = heatmapGrid(CELLS, 60, { startSec: 0, endSec: 180 });
  const color = (count: number) => `c${count}`;

  it("lists each value bucket with events in the column, highest first, in the panel's unit", () => {
    expect(heatmapColumnRows(grid, 0, "ms", color, "Latency")).toEqual([
      { label: "100ms–200ms", value: "1 event", color: "c1" },
      { label: "10ms–20ms", value: "4 events", color: "c4" },
    ]);
  });

  it("says an empty column has no events", () => {
    expect(heatmapColumnRows(grid, 1, "ms", color, "Latency")).toEqual([{ label: "Latency", value: "No events", color: "c0" }]);
  });
});

describe("HeatmapChart hover", () => {
  it("reads a column from the keys on the compact card chart, skipping empty columns", () => {
    render(<HeatmapChart cells={CELLS} unit="ms" height={80} compact label="Latency" stepSeconds={60} window={WINDOW} />);
    const figure = screen.getByRole("group", { name: "Latency" });
    expect(figure).toHaveAttribute("tabindex", "0");

    fireEvent.keyDown(figure, { key: "ArrowLeft" });
    expect(screen.getByTestId("chart-tooltip-time")).toHaveTextContent(formatSampleTime(120));
    expect(screen.getAllByTestId("chart-tooltip-row").map((r) => r.textContent)).toEqual(["7 events"]);
    expect(screen.getByTestId("obs-panel-heatmap-cursor")).toHaveStyle({ gridColumn: "3" });

    // The empty middle column is skipped.
    fireEvent.keyDown(figure, { key: "ArrowLeft" });
    expect(screen.getByTestId("chart-tooltip-time")).toHaveTextContent(formatSampleTime(0));
    expect(screen.getAllByTestId("chart-tooltip-row").map((r) => r.textContent)).toEqual(["100ms–200ms1 event", "10ms–20ms4 events"]);

    fireEvent.keyDown(figure, { key: "Escape" });
    expect(screen.queryByTestId("chart-tooltip")).toBeNull();
    expect(screen.queryByTestId("obs-panel-heatmap-cursor")).toBeNull();
  });

  it("no longer carries a native title per cell: the tooltip replaces it", () => {
    const { container } = render(<HeatmapChart cells={CELLS} unit="ms" height={120} compact={false} label="Latency" stepSeconds={60} window={WINDOW} />);
    expect(container.querySelector("[title]")).toBeNull();
    fireEvent.keyDown(screen.getByRole("group", { name: "Latency" }), { key: "Home" });
    expect(screen.getByTestId("chart-tooltip-time")).toHaveTextContent(formatSampleTime(0));
  });
});
