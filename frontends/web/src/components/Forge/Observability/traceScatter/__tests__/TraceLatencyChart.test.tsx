// Copyright (c) 2025 Reliant Labs

/**
 * The traces page's latency chart, through the real panel and the fixture
 * API: one dot per loaded trace coloured by status, a click opens the trace, a
 * brushed region narrows the list, and the loading, error and empty states.
 */

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { renderPanel } from "../../__tests__/obsTestUtils";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../__tests__/obsMocks")).obsMocks.virtual());

// jsdom has no PointerEvent, so fireEvent.pointer* would build a bare Event
// without coordinates. A MouseEvent carries clientX/clientY and the button.
beforeAll(() => {
  if (typeof window.PointerEvent === "function") return;
  class PointerEventShim extends MouseEvent {
    pointerId: number;
    constructor(type: string, init: PointerEventInit = {}) {
      super(type, init);
      this.pointerId = init.pointerId ?? 0;
    }
  }
  vi.stubGlobal("PointerEvent", PointerEventShim);
});
afterAll(() => vi.unstubAllGlobals());

/** The plot's left and top margins (TraceLatencyChart's SCATTER_LEFT/TOP): a dot's cx/cy are relative to them, and so are pointer coordinates over the surface, whose box jsdom puts at 0,0. */
function dots() {
  return Array.from(screen.getByTestId("obs-trace-latency-dots").querySelectorAll("circle")).map((c) => ({
    id: c.getAttribute("data-trace-id") ?? "",
    status: c.getAttribute("data-status"),
    x: Number(c.getAttribute("cx")),
    y: Number(c.getAttribute("cy")),
  }));
}

function drag(surface: Element, from: { x: number; y: number }, to: { x: number; y: number }) {
  fireEvent.pointerDown(surface, { clientX: from.x, clientY: from.y, button: 0, pointerId: 1 });
  fireEvent.pointerMove(surface, { clientX: to.x, clientY: to.y, pointerId: 1 });
  fireEvent.pointerUp(surface, { clientX: to.x, clientY: to.y, pointerId: 1 });
}

describe("trace latency chart", () => {
  it("plots every loaded trace once, blue when it succeeded and red when it failed", async () => {
    renderPanel(createFixtureApi(), { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    const plotted = dots();
    expect(plotted).toHaveLength(40);
    expect(new Set(plotted.map((d) => d.id)).size).toBe(40);
    // The fixture fails every ninth trace from the fifth: 4, 13, 22, 31.
    expect(plotted.filter((d) => d.status === "error")).toHaveLength(4);
    const errorDot = screen.getByTestId("obs-trace-latency-dots").querySelector('circle[data-status="error"]');
    expect(errorDot).toHaveClass("fill-danger");
    expect(screen.getByTestId("obs-trace-latency-dots").querySelector('circle[data-status="ok"]')).toHaveClass("fill-info");
    expect(screen.getByTestId("obs-trace-latency-coverage")).toHaveTextContent("Plotting all 40 matching traces");
  });

  it("marks p50, p95 and p99 on a histogram split into successes and failures", async () => {
    renderPanel(createFixtureApi(), { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    const histogram = screen.getByTestId("obs-trace-latency-histogram");
    for (const k of ["p50", "p95", "p99"]) expect(within(histogram).getByTestId(`obs-trace-latency-${k}`)).toHaveTextContent(k);
    const buckets = within(histogram).getAllByTestId("obs-trace-latency-bucket");
    const sum = (key: "data-ok" | "data-error") => buckets.reduce((n, b) => n + Number(b.getAttribute(key)), 0);
    expect(sum("data-ok")).toBe(36);
    expect(sum("data-error")).toBe(4);
  });

  it("describes the plot in words for a screen reader", async () => {
    renderPanel(createFixtureApi(), { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    expect(screen.getByTestId("obs-trace-latency-plot").querySelector("figcaption")).toHaveTextContent(
      /40 traces plotted by start time and root-span latency, 4 with errors\. Median .+, p95 .+, p99 .+\./
    );
  });

  it("opens the trace whose dot is clicked", async () => {
    const harness = renderPanel(createFixtureApi(), { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    const target = dots()[7];
    const surface = screen.getByTestId("obs-trace-latency-surface");
    drag(surface, target, target);
    expect(harness.current().trace).toBe(target.id);
  });

  it("narrows the list to the traces inside a brushed region, and clears it", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    const plotted = dots();
    // A box around the newer half of the traces, every latency.
    const xs = plotted.map((d) => d.x).sort((a, b) => a - b);
    const from = { x: xs[20] - 1, y: 0 };
    const to = { x: xs[39] + 1, y: 200 };
    const inside = plotted.filter((d) => d.x >= from.x && d.x <= to.x);
    drag(screen.getByTestId("obs-trace-latency-surface"), from, to);

    const bar = await screen.findByTestId("obs-trace-region");
    expect(bar).toHaveTextContent(`${inside.length} of 40`);
    expect(screen.getAllByTestId("obs-trace-row")).toHaveLength(inside.length);
    expect(screen.getByTestId("obs-trace-latency-brush")).toBeInTheDocument();

    await user.click(screen.getByTestId("obs-trace-region-clear"));
    expect(screen.queryByTestId("obs-trace-region")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("obs-trace-row")).toHaveLength(40);
  });

  it("says so when a brushed region holds none of the loaded traces", async () => {
    renderPanel(createFixtureApi(), { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    // The top-left corner: the oldest instant at the slowest latency the axis shows.
    drag(screen.getByTestId("obs-trace-latency-surface"), { x: 0, y: 0 }, { x: 6, y: 6 });
    expect(await screen.findByTestId("obs-trace-region-empty")).toHaveTextContent("No loaded traces fall in the selected region");
    expect(screen.queryAllByTestId("obs-trace-row")).toHaveLength(0);
  });

  it("drops the region when the filters change", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    drag(screen.getByTestId("obs-trace-latency-surface"), { x: 0, y: 0 }, { x: 600, y: 200 });
    await screen.findByTestId("obs-trace-region");
    await user.click(screen.getByTestId("obs-errors-only"));
    await waitFor(() => expect(screen.queryByTestId("obs-trace-region")).not.toBeInTheDocument());
  });

  it("switches the latency axis between log and linear", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    const log = screen.getByTestId("obs-trace-latency-scale-log");
    const linear = screen.getByTestId("obs-trace-latency-scale-linear");
    expect(log).toHaveAttribute("aria-pressed", "true");
    const before = dots().map((d) => d.y);
    await user.click(linear);
    expect(linear).toHaveAttribute("aria-pressed", "true");
    expect(log).toHaveAttribute("aria-pressed", "false");
    expect(dots().map((d) => d.y)).not.toEqual(before);
  });

  it("holds the plot's place while the first page loads", async () => {
    const api = createFixtureApi();
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    const listTraces = api.listTraces.bind(api);
    api.listTraces = async (req, options) => {
      await gate;
      return listTraces(req, options);
    };
    renderPanel(api, { view: "traces" });
    expect(await screen.findByTestId("obs-trace-latency-loading")).toHaveAttribute("aria-busy", "true");
    release();
    await screen.findAllByTestId("obs-trace-row");
    expect(screen.queryByTestId("obs-trace-latency-loading")).not.toBeInTheDocument();
  });

  it("says nothing was traced, rather than drawing empty axes, when the window is empty", async () => {
    renderPanel(createFixtureApi({ empty: true }), { view: "traces" });
    expect(await screen.findByTestId("obs-trace-latency-empty")).toHaveTextContent("No traces to plot");
  });

  it("points at the page's error, without a second retry, when the traces fail to load", async () => {
    renderPanel(createFixtureApi({ failWith: Code.Unavailable }), { view: "traces" });
    const error = await screen.findByTestId("obs-trace-latency-error", undefined, { timeout: 5000 });
    expect(error).toHaveTextContent("The latency chart could not be drawn");
    expect(within(error).queryByRole("button")).not.toBeInTheDocument();
  });
});
