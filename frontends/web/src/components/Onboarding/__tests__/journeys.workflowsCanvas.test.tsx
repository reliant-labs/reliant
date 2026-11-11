/**
 * The workflows journey on the canvas (journeys/workflowsCanvas.tsx and
 * workflow/canvas/tourCanvas.ts):
 *
 *  - the callout never overlaps the steps it explains, nor their dots: a
 *    sweep over screen sizes and every step's group, through the same pan
 *    the builder applies and the engine's own placement; and once in the DOM,
 *    with the real spotlight against the real focus box;
 *  - the drag demonstration loops through its beats, and under
 *    prefers-reduced-motion is a still diagram that never animates;
 *  - Try it yourself lends the edge and puts it back.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import React from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { BESIDE_FIRST, overlapArea, placeCallout, type Box } from "../calloutPlacement";
import { OnboardingSpotlight } from "../OnboardingSpotlight";
import { tourEvents } from "../tourEvents";
import { CANVAS_RESERVE, CARD_ROOM, DEMO_CYCLE, DragDemo, EdgeLesson, FOCUS_TARGET, OnCanvas, demoFrame } from "../journeys/workflowsCanvas";
import { tourCanvas, unionRect, viewportForFocus, type Rect } from "../../workflow/canvas/tourCanvas";

// ─── Geometry: the pan, then the engine's placement ──────────────────────────

/** The builder's header: the canvas starts this far down the window. */
const CANVAS_TOP = 122;
/** React Flow's dots stick out of a node's side by about this much. */
const HANDLE_OVERHANG = 8;
/** OnCanvas's padding around the box, and the step's spotlight padding. */
const BOX_PADDING = 6 + 4;
const CARD_HEIGHTS = [180, 260, 360, 440];
const SCREENS = [
  { width: 1280, height: 720 },
  { width: 1366, height: 768 },
  { width: 1440, height: 900 },
  { width: 1680, height: 1050 },
  { width: 1920, height: 1080 },
  { width: 2560, height: 1440 },
];

/** Each canvas step's group, as Fix my PR lays out (world px; node 220×90, rail 200×170). */
const GROUPS: Record<string, Rect[]> = {
  "wf-example": [{ x: 300, y: 0, width: 220, height: 90 }],
  "wf-edge": [
    { x: 300, y: 0, width: 220, height: 90 },
    { x: 600, y: 160, width: 260, height: 140 },
  ],
  "wf-loop": [{ x: 600, y: 160, width: 260, height: 140 }],
  "wf-branch": [
    { x: 940, y: 0, width: 160, height: 140 },
    { x: 1180, y: -40, width: 180, height: 50 },
    { x: 1180, y: 110, width: 180, height: 100 },
    { x: 1420, y: 280, width: 180, height: 100 },
  ],
  "wf-trigger": [
    { x: 0, y: 20, width: 230, height: 170 },
    { x: 300, y: 0, width: 220, height: 90 },
  ],
};

function onScreen(world: Rect, vp: { x: number; y: number; zoom: number }): Box {
  return {
    left: vp.x + world.x * vp.zoom - HANDLE_OVERHANG - BOX_PADDING,
    top: CANVAS_TOP + vp.y + world.y * vp.zoom - BOX_PADDING,
    width: world.width * vp.zoom + 2 * (HANDLE_OVERHANG + BOX_PADDING),
    height: world.height * vp.zoom + 2 * BOX_PADDING,
  };
}

describe("the callout never covers what it explains", () => {
  for (const [stepId, nodes] of Object.entries(GROUPS)) {
    it(`${stepId}: beside its steps and their dots, on every screen and card height`, () => {
      const group = unionRect(nodes)!;
      for (const screenSize of SCREENS) {
        const canvas = { width: screenSize.width, height: screenSize.height - CANVAS_TOP };
        const vp = viewportForFocus(group, canvas, CANVAS_RESERVE);
        const target = onScreen(group, vp);
        // The whole group is on screen, clear of the palette and of the card's room.
        expect(target.left, `${stepId} ${screenSize.width}`).toBeGreaterThanOrEqual(CANVAS_RESERVE.left - HANDLE_OVERHANG - BOX_PADDING - 0.5);
        expect(target.left + target.width).toBeLessThanOrEqual(screenSize.width - CARD_ROOM + 0.5);
        for (const height of CARD_HEIGHTS) {
          const placed = placeCallout(target, { width: 320, height }, screenSize, {
            preferred: "right",
            order: BESIDE_FIRST,
            gap: 16,
            margin: 16,
          });
          expect(placed.overlaps, `${stepId} ${screenSize.width}×${screenSize.height} card ${height}`).toBe(false);
          expect(placed.side).toBe("right");
          expect(overlapArea(target, { top: placed.top, left: placed.left, width: 320, height })).toBe(0);
          // Nor over any one step inside the group.
          for (const node of nodes) expect(overlapArea(onScreen(node, vp), { top: placed.top, left: placed.left, width: 320, height })).toBe(0);
        }
      }
    });
  }

  it("never zooms a step larger than life, and keeps a big group readable", () => {
    const one = viewportForFocus(GROUPS["wf-loop"][0], { width: 1440, height: 778 }, CANVAS_RESERVE);
    expect(one.zoom).toBe(1);
    const branch = viewportForFocus(unionRect(GROUPS["wf-branch"])!, { width: 1280, height: 598 }, CANVAS_RESERVE);
    expect(branch.zoom).toBeGreaterThan(0.45);
  });
});

// ─── In the DOM: the focus box and the real spotlight ────────────────────────

function rectOf(r: { left: number; top: number; width: number; height: number }) {
  return () =>
    ({ ...r, x: r.left, y: r.top, right: r.left + r.width, bottom: r.top + r.height, toJSON: () => r }) as DOMRect;
}

function addNode(id: string, r: { left: number; top: number; width: number; height: number }) {
  const node = document.createElement("div");
  node.className = "react-flow__node";
  node.dataset.id = id;
  node.getBoundingClientRect = rectOf(r);
  const source = document.createElement("div");
  source.className = "react-flow__handle source";
  source.dataset.nodeid = id;
  source.getBoundingClientRect = rectOf({ left: r.left + r.width - 4, top: r.top + r.height / 2 - 4, width: 8, height: 8 });
  const target = document.createElement("div");
  target.className = "react-flow__handle target";
  target.dataset.nodeid = id;
  target.getBoundingClientRect = rectOf({ left: r.left - 4, top: r.top + r.height / 2 - 4, width: 8, height: 8 });
  node.append(source, target);
  document.body.append(node);
  return node;
}

function setMotion(reduce: boolean) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: reduce && query.includes("reduce"),
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })) as never;
}

const originalMatchMedia = window.matchMedia;
beforeEach(() => {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 1440 });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 900 });
  setMotion(false);
});
afterEach(() => {
  document.querySelectorAll(".react-flow__node").forEach((n) => n.remove());
  window.matchMedia = originalMatchMedia;
  vi.restoreAllMocks();
});

describe("OnCanvas", () => {
  it("asks the builder to pan to its steps, boxes them, and the spotlight's card lands beside the box", async () => {
    const focus = vi.spyOn(tourCanvas, "focus");
    // jsdom has no layout: the focus box measures where its style puts it, as a browser would.
    const measure = Element.prototype.getBoundingClientRect;
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const el = this as HTMLElement;
      if (el.dataset?.tour !== "wf-focus") return measure.call(el);
      const r = { left: parseFloat(el.style.left), top: parseFloat(el.style.top), width: parseFloat(el.style.width), height: parseFloat(el.style.height) };
      return rectOf(r)();
    });
    addNode("review", { left: 300, top: 300, width: 220, height: 90 });
    addNode("fix_until_green", { left: 600, top: 420, width: 260, height: 140 });
    render(
      <OnboardingSpotlight
        targetSelector={FOCUS_TARGET}
        title="Edges set the order"
        description={<OnCanvas nodes={["review", "fix_until_green"]} />}
        stepNumber={2}
        totalSteps={8}
        onNext={vi.fn()}
        onSkipAll={vi.fn()}
        tooltipPosition="right"
        callout={{ footer: null }}
      />,
    );
    expect(focus).toHaveBeenCalledWith(expect.objectContaining({ nodeIds: ["review", "fix_until_green"], reserve: CANVAS_RESERVE }));
    const box = await screen.findByTestId("wf-focus");
    expect(box).toHaveAttribute("data-tour", "wf-focus");
    expect(box.style).toMatchObject({ left: "294px", top: "294px", width: "572px", height: "272px" });

    const callout = screen.getByTestId("tour-callout");
    await waitFor(() => expect(callout).toHaveAttribute("data-side", "right"), { timeout: 3000 });
    const placed = { left: parseFloat(callout.style.left), top: parseFloat(callout.style.top), width: 320, height: 200 };
    const target = { left: 294, top: 294, width: 572, height: 272 };
    expect(overlapArea(target, placed)).toBe(0);
    expect(placed.left).toBeGreaterThanOrEqual(target.left + target.width);
  });

  it("releases the canvas when the step closes", () => {
    const release = vi.spyOn(tourCanvas, "release");
    const { unmount } = render(<OnCanvas nodes={["review"]} />);
    unmount();
    expect(release).toHaveBeenCalled();
  });
});

// ─── The drag demonstration ──────────────────────────────────────────────────

describe("the drag demonstration", () => {
  it("presses the dot, drags, draws the edge, then takes it away, and loops", () => {
    expect(demoFrame(0)).toMatchObject({ drawn: 0, solid: false, hideReal: false });
    expect(demoFrame(400).pressed).toBe(true);
    const mid = demoFrame(1250);
    expect(mid.drawn).toBeGreaterThan(0.2);
    expect(mid.drawn).toBeLessThan(0.8);
    expect(mid.hideReal).toBe(true);
    expect(demoFrame(2200)).toMatchObject({ drawn: 1, solid: true, ghost: 1 });
    expect(demoFrame(3200)).toMatchObject({ ghost: 0, hideReal: false });
    expect(demoFrame(DEMO_CYCLE + 1250)).toEqual(mid);
  });

  it("animates over the real dots", async () => {
    addNode("review", { left: 300, top: 300, width: 220, height: 90 });
    addNode("fix_until_green", { left: 600, top: 420, width: 260, height: 140 });
    render(<DragDemo source="review" target="fix_until_green" />);
    const svg = await screen.findByTestId("wf-drag-demo");
    expect(svg).toHaveAttribute("data-motion", "animated");
    expect(screen.queryByTestId("wf-drag-demo-static")).toBeNull();
  });

  it("is a still diagram under prefers-reduced-motion, and never touches the real edge", async () => {
    setMotion(true);
    addNode("review", { left: 300, top: 300, width: 220, height: 90 });
    addNode("fix_until_green", { left: 600, top: 420, width: 260, height: 140 });
    const edge = document.createElementNS("http://www.w3.org/2000/svg", "g");
    edge.setAttribute("class", "react-flow__edge");
    edge.setAttribute("aria-label", "Edge from review to fix_until_green");
    document.body.append(edge);
    render(<DragDemo source="review" target="fix_until_green" />);
    const svg = await screen.findByTestId("wf-drag-demo");
    expect(svg).toHaveAttribute("data-motion", "static");
    const still = screen.getByTestId("wf-drag-demo-static");
    expect(still.querySelector("path[stroke-dasharray]")?.getAttribute("d")).toMatch(/^M 520 345 C/);
    // Let some frames pass (fake clock: no real sleep orders this): nothing
    // moves, and the real edge stays as it is.
    const before = svg.innerHTML;
    vi.useFakeTimers({ toFake: ["setTimeout", "setInterval", "requestAnimationFrame", "cancelAnimationFrame", "performance", "Date"] });
    try {
      act(() => vi.advanceTimersByTime(500));
    } finally {
      vi.useRealTimers();
    }
    expect(svg.innerHTML).toBe(before);
    expect(edge.style.opacity).toBe("");
    edge.remove();
  });
});

describe("Try it yourself", () => {
  it("lends the edge (the demonstration steps aside), and puts it back if it isn't drawn", () => {
    const detach = vi.spyOn(tourCanvas, "detachEdge");
    const reattach = vi.spyOn(tourCanvas, "reattachEdge");
    addNode("review", { left: 300, top: 300, width: 220, height: 90 });
    addNode("fix_until_green", { left: 600, top: 420, width: 260, height: 140 });
    const { unmount } = render(<EdgeLesson source="review" target="fix_until_green" />);
    fireEvent.click(screen.getByTestId("wf-try-edge"));
    expect(detach).toHaveBeenCalledWith("review", "fix_until_green");
    expect(screen.getByTestId("wf-try-instructions")).toHaveTextContent(/drag from the dot on the right of review/i);
    expect(screen.queryByTestId("wf-drag-demo")).toBeNull();
    unmount();
    expect(reattach).toHaveBeenCalledTimes(1);
  });

  it("leaves a drawn edge alone", () => {
    const reattach = vi.spyOn(tourCanvas, "reattachEdge");
    const { unmount } = render(<EdgeLesson source="review" target="fix_until_green" />);
    fireEvent.click(screen.getByTestId("wf-try-edge"));
    tourEvents.emit("workflow:edgeConnected", { sourceId: "review", targetId: "fix_until_green" });
    unmount();
    expect(reattach).not.toHaveBeenCalled();
  });
});

describe("tourCanvas", () => {
  it("remembers the last focus for a builder that loads after the ask, until released", () => {
    const seen = vi.fn();
    const off = tourCanvas.subscribe(seen);
    const focus = { nodeIds: ["review"], reserve: CANVAS_RESERVE };
    tourCanvas.focus(focus);
    expect(seen).toHaveBeenCalledWith({ kind: "focus", focus });
    expect(tourCanvas.pendingFocus()).toBe(focus);
    tourCanvas.release();
    expect(tourCanvas.pendingFocus()).toBeNull();
    off();
  });
});

void React;
