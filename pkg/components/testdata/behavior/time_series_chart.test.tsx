// Behaviour of time_series_chart in a real DOM (jsdom): series paths with
// gaps, annotation markers, and the keyboard scrubber's readout. Run by
// TestComponentBehaviour.
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test } from "vitest";
import TimeSeriesChart, { type TimeSeries } from "./time_series_chart";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
});

const series: TimeSeries[] = [
  {
    id: "visits",
    label: "Visits",
    variant: "area",
    points: [
      { t: "2026-11-01", value: 100 },
      { t: "2026-11-02", value: 140 },
      { t: "2026-11-03", value: null },
      { t: "2026-11-04", value: 900 },
      { t: "2026-11-05", value: 400 },
    ],
  },
  {
    id: "signups",
    label: "Signups",
    points: [
      { t: "2026-11-01", value: 3 },
      { t: "2026-11-04", value: 41 },
    ],
  },
];

function render(ui: React.ReactElement) {
  act(() => root.render(ui));
}

const slider = () => document.querySelector('[role="slider"]') as HTMLElement;

function keydown(key: string) {
  act(() => {
    slider().dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  });
}

test("each series draws a line, a null value splits it, and areas get a fill", () => {
  render(<TimeSeriesChart ariaLabel="Traffic" series={series} />);
  const strokes = [...document.querySelectorAll('path[fill="none"]')];
  expect(strokes).toHaveLength(2);
  // Visits has a gap on Nov 3: two subpaths.
  expect(strokes[0]?.getAttribute("d")?.match(/M/g)).toHaveLength(2);
  expect(document.querySelectorAll('path[fill^="url(#"]')).toHaveLength(1);
  expect(document.querySelectorAll("linearGradient")).toHaveLength(2);
});

test("annotations draw a labelled marker", () => {
  render(
    <TimeSeriesChart
      ariaLabel="Traffic"
      series={series}
      annotations={[{ t: "2026-11-04", label: "Posted on HN" }]}
    />,
  );
  const labels = [...document.querySelectorAll("svg text")].map((t) => t.textContent);
  expect(labels).toContain("Posted on HN");
  expect(document.querySelectorAll('line[stroke-dasharray="3 3"]')).toHaveLength(1);
});

test("the keyboard scrubs through time and reads every series and annotation", () => {
  render(
    <TimeSeriesChart
      ariaLabel="Traffic"
      series={series}
      annotations={[{ t: "2026-11-04", label: "Posted on HN" }]}
      formatValue={(v) => String(v)}
      formatTime={(d) => `Nov ${d.getDate()}`}
    />,
  );
  act(() => slider().focus());
  // Focus starts on the latest point.
  expect(slider().getAttribute("aria-valuenow")).toBe("4");
  expect(slider().getAttribute("aria-valuetext")).toBe("Nov 5, Visits 400");
  keydown("ArrowLeft");
  expect(slider().getAttribute("aria-valuetext")).toBe(
    "Nov 4, Visits 900, Signups 41, Posted on HN",
  );
  keydown("Home");
  expect(slider().getAttribute("aria-valuetext")).toBe("Nov 1, Visits 100, Signups 3");
  expect(document.querySelectorAll("svg circle")).toHaveLength(2);
  act(() => slider().blur());
  expect(document.querySelectorAll("svg circle")).toHaveLength(0);
});

test("no points renders the empty message instead of axes", () => {
  render(
    <TimeSeriesChart
      ariaLabel="Traffic"
      series={[{ id: "a", label: "A", points: [] }]}
      emptyMessage="Nothing yet"
    />,
  );
  expect(document.body.textContent).toBe("Nothing yet");
  expect(slider()).toBeNull();
});
