// Copyright (c) 2026 Reliant Labs

/**
 * HeadlineBars reads the bucket under the pointer like every chart: the
 * mouse, a finger, or the arrow keys on the focused figure highlight one
 * bucket, and a tooltip states its time and count in the caller's words.
 */

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import { formatSampleTime } from "../chartHover";
import { HeadlineBars } from "../HeadlineBars";

const HOUR = 3600;
const TIMES = [10 * HOUR, 11 * HOUR, 12 * HOUR, 13 * HOUR];
const runs = (n: number) => `${n} run${n === 1 ? "" : "s"} started`;

describe("HeadlineBars hover", () => {
  // jsdom has no PointerEvent: a MouseEvent carries clientX.
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

  const tooltipRow = () => screen.getByTestId("chart-tooltip-row").textContent;

  it("is a focusable, named figure whose arrow keys step through the buckets, zero ones included", () => {
    render(<HeadlineBars values={[3, 0, 1, 5]} timestamps={TIMES} label="Runs started, last 4 hours" format={runs} />);
    const figure = screen.getByRole("group", { name: "Runs started, last 4 hours" });
    expect(figure).toHaveAttribute("tabindex", "0");
    expect(screen.queryByTestId("chart-tooltip")).toBeNull();

    fireEvent.keyDown(figure, { key: "ArrowLeft" });
    // From no highlight: the latest bucket.
    expect(screen.getByTestId("chart-tooltip-time")).toHaveTextContent(formatSampleTime(TIMES[3]!));
    expect(tooltipRow()).toBe("5 runs started");
    expect(screen.getByTestId("headline-bars-cursor")).toHaveAttribute("x", "30");

    fireEvent.keyDown(figure, { key: "ArrowLeft" });
    fireEvent.keyDown(figure, { key: "ArrowLeft" });
    // An empty bucket is still a reading: nothing started that hour.
    expect(tooltipRow()).toBe("0 runs started");
    expect(screen.getByText(`${formatSampleTime(TIMES[1]!)}: 0 runs started.`)).toBeInTheDocument();

    fireEvent.keyDown(figure, { key: "Escape" });
    expect(screen.queryByTestId("chart-tooltip")).toBeNull();
  });

  it("reads the bucket under the mouse and a finger, and drops the mouse's when it leaves", () => {
    render(<HeadlineBars values={[3, 0, 1, 5]} timestamps={TIMES} label="Runs" format={runs} />);
    const svg = screen.getByRole("group", { name: "Runs" }).querySelector("svg")!;
    vi.spyOn(svg, "getBoundingClientRect").mockReturnValue({ left: 0, top: 0, right: 400, bottom: 112, width: 400, height: 112, x: 0, y: 0, toJSON: () => ({}) });
    const figure = screen.getByRole("group", { name: "Runs" });

    fireEvent.pointerMove(svg, { clientX: 50, pointerType: "mouse" });
    expect(tooltipRow()).toBe("3 runs started");
    fireEvent.pointerLeave(figure, { pointerType: "mouse" });
    expect(screen.queryByTestId("chart-tooltip")).toBeNull();

    fireEvent.pointerDown(svg, { clientX: 250, pointerType: "touch" });
    expect(tooltipRow()).toBe("1 run started");
  });
});
