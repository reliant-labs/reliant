// Copyright (c) 2026 Reliant Labs

/**
 * The word-sized sparkline reads a sample like every observability chart
 * (charts/useChartHover): the keys step through it, the mouse hovers it, and
 * the tooltip states the sample's time (or its position, with no timestamps)
 * and its value, formatted as the row beside it formats it.
 */

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import { formatSampleTime } from "../../Observability/charts/chartHover";
import { Sparkline } from "../Sparkline";

const TIMES = [1000, 1060, 1120, 1180];

beforeAll(() => {
  if (typeof window.PointerEvent === "function") return;
  class PointerEventShim extends MouseEvent {
    pointerType: string;
    constructor(type: string, init: PointerEventInit = {}) {
      super(type, init);
      this.pointerType = init.pointerType ?? "mouse";
    }
  }
  vi.stubGlobal("PointerEvent", PointerEventShim);
});
afterAll(() => vi.unstubAllGlobals());

describe("Sparkline hover", () => {
  it("is focusable, steps with the arrow keys and announces the sample with its time and formatted value", () => {
    render(<Sparkline values={[1, 2, null, 4]} timestamps={TIMES} format={(v) => `${v} req/s`} label="Requests per second to api" />);
    const line = screen.getByRole("img", { name: "Requests per second to api" });
    expect(line).toHaveAttribute("tabindex", "0");

    fireEvent.keyDown(line, { key: "ArrowLeft" });
    expect(screen.getByTestId("chart-tooltip-time")).toHaveTextContent(formatSampleTime(TIMES[3]!));
    expect(screen.getByTestId("chart-tooltip")).toHaveTextContent("4 req/s");
    expect(screen.getByTestId("sparkline-cursor")).toBeInTheDocument();
    expect(screen.getByText(`${formatSampleTime(TIMES[3]!)}: 4 req/s.`)).toBeInTheDocument();

    // The empty sample is skipped.
    fireEvent.keyDown(line, { key: "ArrowLeft" });
    expect(screen.getByTestId("chart-tooltip")).toHaveTextContent("2 req/s");

    fireEvent.keyDown(line, { key: "Escape" });
    expect(screen.queryByTestId("chart-tooltip")).toBeNull();
  });

  it("follows the mouse and clears when it leaves, naming the sample's position without timestamps", () => {
    render(<Sparkline values={[1, 2, 3, 4, 5]} label="Coverage" />);
    const line = screen.getByRole("img", { name: "Coverage" });
    vi.spyOn(line, "getBoundingClientRect").mockReturnValue({ left: 0, top: 0, width: 64, height: 18, right: 64, bottom: 18, x: 0, y: 0, toJSON: () => ({}) });
    fireEvent.pointerMove(line, { pointerType: "mouse", clientX: 32 });
    expect(screen.getByTestId("chart-tooltip-time")).toHaveTextContent("3 of 5");
    expect(screen.getByTestId("chart-tooltip")).toHaveTextContent("3");
    fireEvent.pointerLeave(line, { pointerType: "mouse" });
    expect(screen.queryByTestId("chart-tooltip")).toBeNull();
  });

  it("draws a dash, not a line, for fewer than two samples", () => {
    render(<Sparkline values={[null, 3]} label="CPU" />);
    expect(screen.getByLabelText("CPU: no samples")).toHaveTextContent("—");
  });
});
