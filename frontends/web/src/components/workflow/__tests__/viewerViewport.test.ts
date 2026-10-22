import { describe, expect, it } from "vitest";

import {
  FOCUS_MIN_ZOOM,
  OVERVIEW_MIN_ZOOM,
  frameBounds,
  isInView,
  runningFocusNodeIds,
} from "../viewerViewport";

// The side panel the viewer opens in by default (ChatPresenter) is 400px wide.
const SIDE_PANEL = { width: 400, height: 700 };

describe("frameBounds", () => {
  it("never frames a wide workflow below the readable floor", () => {
    // Get It Right with its loop expanded: ~2600px wide. Fit-all would need
    // ~0.13 zoom in a 400px panel, which is what made the labels unreadable.
    const graph = { x: 0, y: 0, width: 2600, height: 900 };
    const viewport = frameBounds(graph, SIDE_PANEL, { minZoom: OVERVIEW_MIN_ZOOM });

    expect(viewport.zoom).toBe(OVERVIEW_MIN_ZOOM);
    // It does not fit, so the START of the graph is what is on screen.
    expect(isInView({ x: 0, y: 0, width: 200, height: 80 }, viewport, SIDE_PANEL)).toBe(true);
  });

  it("centres a graph that fits, without blowing it up past natural size", () => {
    const graph = { x: 100, y: 50, width: 200, height: 100 };
    const viewport = frameBounds(graph, SIDE_PANEL, { minZoom: OVERVIEW_MIN_ZOOM });

    expect(viewport.zoom).toBe(1);
    expect(viewport.x).toBe((SIDE_PANEL.width - 200) / 2 - 100);
    expect(viewport.y).toBe((SIDE_PANEL.height - 100) / 2 - 50);
  });

  it("brings the running step fully into view at the focus zoom", () => {
    const runningStep = { x: 1800, y: 400, width: 260, height: 90 };
    const viewport = frameBounds(runningStep, SIDE_PANEL, { minZoom: FOCUS_MIN_ZOOM });

    expect(viewport.zoom).toBeGreaterThanOrEqual(FOCUS_MIN_ZOOM);
    expect(isInView(runningStep, viewport, SIDE_PANEL)).toBe(true);
  });
});

describe("runningFocusNodeIds", () => {
  it("focuses the running steps, not the loop container around them", () => {
    const ids = runningFocusNodeIds([
      { id: "attempt", type: "expandedLoopNode", data: { executionStatus: "running" } },
      { id: "attempt:test", type: "runNode", data: { executionStatus: "running" } },
      { id: "attempt:lint", type: "runNode", data: { executionStatus: "completed" } },
      { id: "attempt:build", type: "runNode", data: { executionStatus: "running" } },
    ]);
    expect(ids).toEqual(["attempt:build", "attempt:test"]);
  });

  it("falls back to a running container when nothing inside is known to run", () => {
    const ids = runningFocusNodeIds([
      { id: "attempt", type: "expandedLoopNode", data: { executionStatus: "running" } },
      { id: "attempt:test", type: "runNode", data: {} },
    ]);
    expect(ids).toEqual(["attempt"]);
  });

  it("has no focus when nothing runs", () => {
    expect(runningFocusNodeIds([{ id: "a", data: { executionStatus: "completed" } }])).toEqual([]);
  });
});
