// Behaviour of cohort_grid in a real DOM (jsdom): percentages against the
// cohort size, the triangle's not-yet cells, small-n dimming and the
// size-weighted average row. Run by TestComponentBehaviour.
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test } from "vitest";
import CohortGrid, { type Cohort } from "./cohort_grid";

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

const cohorts: Cohort[] = [
  { label: "Oct 6", size: 400, retained: [400, 200, 100] },
  { label: "Oct 13", size: 100, retained: [100, 30] },
  { label: "Oct 20", size: 10, retained: [10] },
];

function render(ui: React.ReactElement) {
  act(() => root.render(ui));
}

/** The visible text of each data cell in a row, "" for a not-yet cell. */
function cells(row: Element): string[] {
  return [...row.querySelectorAll("td")].slice(1).map((td) => {
    const box = td.querySelector("div");
    return box
      ? [...box.querySelectorAll(":scope > span:not(.sr-only)")].map((s) => s.textContent).join(" ")
      : "";
  });
}

test("cells show the share of the cohort and its count; future periods are empty", () => {
  render(<CohortGrid cohorts={cohorts} showAverage={false} />);
  const headers = [...document.querySelectorAll("thead th")].map((th) => th.textContent);
  expect(headers).toEqual(["Cohort", "Users", "Week 0", "Week 1", "Week 2"]);
  const rows = [...document.querySelectorAll("tbody tr")];
  expect(cells(rows[0] as Element)).toEqual(["100% n = 400", "50% n = 200", "25% n = 100"]);
  expect(cells(rows[1] as Element)).toEqual(["100% n = 100", "30% n = 30", ""]);
  expect(cells(rows[2] as Element)).toEqual(["100% n = 10", "", ""]);
  expect(rows[2]?.textContent).toContain("Not yet");
});

test("cohorts smaller than minN are dimmed and marked", () => {
  render(<CohortGrid cohorts={cohorts} minN={50} showAverage={false} />);
  const rows = [...document.querySelectorAll("tbody tr")];
  expect(rows[2]?.querySelector("td div")?.className).toContain("opacity-40");
  expect(rows[2]?.textContent).toContain("small sample");
  expect(rows[0]?.querySelector("td div")?.className).not.toContain("opacity-40");
});

test("dimBy cell compares each cell's own count", () => {
  render(<CohortGrid cohorts={cohorts} minN={50} dimBy="cell" showAverage={false} />);
  const first = document.querySelector("tbody tr") as Element;
  const boxes = [...first.querySelectorAll("td div")];
  expect(boxes.map((b) => b.className.includes("opacity-40"))).toEqual([false, false, false]);
  const second = document.querySelectorAll("tbody tr")[1] as Element;
  expect(
    [...second.querySelectorAll("td div")].map((b) => b.className.includes("opacity-40")),
  ).toEqual([false, true]);
});

test("the average row weights each period by the cohorts that reached it", () => {
  render(<CohortGrid cohorts={cohorts} />);
  const avg = document.querySelector("tfoot tr") as Element;
  expect(avg.querySelector("th")?.textContent).toBe("All cohorts");
  // Week 1: (200 + 30) / (400 + 100) = 46%; week 2: 100 / 400 = 25%.
  expect(cells(avg)).toEqual(["100% n = 510", "46% n = 230", "25% n = 100"]);
});
