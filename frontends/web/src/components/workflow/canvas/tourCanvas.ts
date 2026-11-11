/**
 * What the onboarding tour may ask of the open workflow canvas, without
 * reaching into the builder's state: pan and zoom to some steps, and take one
 * edge out for the user to draw again ("Try it yourself", then put it back).
 *
 * A module-level channel rather than context, because the tour's cards are
 * rendered by the journey engine, outside the builder's tree. The builder
 * listens through `useTourCanvas` (hooks/useTourCanvas.ts); nothing here
 * touches React, so the viewport arithmetic is unit-tested on its own
 * (__tests__/tourCanvas.test.ts).
 */

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** Screen pixels to keep free on each side of the canvas: room for the tour's card and the builder's own chrome. */
export interface Reserve {
  top: number;
  right: number;
  bottom: number;
  left: number;
}

export interface Viewport {
  x: number;
  y: number;
  zoom: number;
}

export interface TourCanvasFocus {
  /** The steps to bring into view, by node id. */
  nodeIds: string[];
  reserve: Reserve;
  /** World pixels around the steps. Default 32. */
  padding?: number;
  /** Default 1: never larger than life. */
  maxZoom?: number;
  /** Animate the pan (off under prefers-reduced-motion). */
  animate?: boolean;
}

export type TourCanvasCommand =
  | { kind: "focus"; focus: TourCanvasFocus }
  | { kind: "detachEdge"; source: string; target: string }
  | { kind: "reattachEdge" };

type Listener = (command: TourCanvasCommand) => void;

const listeners = new Set<Listener>();
/** The last focus asked for, so a builder that mounts (or finishes loading) after the ask still applies it. */
let lastFocus: TourCanvasFocus | null = null;

export const tourCanvas = {
  focus(focus: TourCanvasFocus): void {
    lastFocus = focus;
    listeners.forEach((listener) => listener({ kind: "focus", focus }));
  },
  /** Take the edge from `source` to `target` off the canvas, unsaved, until it is drawn again or reattached. */
  detachEdge(source: string, target: string): void {
    listeners.forEach((listener) => listener({ kind: "detachEdge", source, target }));
  },
  /** Put a detached edge back, if the user didn't draw it. */
  reattachEdge(): void {
    listeners.forEach((listener) => listener({ kind: "reattachEdge" }));
  },
  /** Forget the pending focus (the tour left the canvas). */
  release(): void {
    lastFocus = null;
  },
  pendingFocus(): TourCanvasFocus | null {
    return lastFocus;
  },
  subscribe(listener: Listener): () => void {
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  },
};

/** The smallest world rectangle holding every rect. Null for none. */
export function unionRect(rects: Rect[]): Rect | null {
  if (rects.length === 0) return null;
  const left = Math.min(...rects.map((r) => r.x));
  const top = Math.min(...rects.map((r) => r.y));
  const right = Math.max(...rects.map((r) => r.x + r.width));
  const bottom = Math.max(...rects.map((r) => r.y + r.height));
  return { x: left, y: top, width: right - left, height: bottom - top };
}

/**
 * The viewport that shows `box` (world coordinates) as large as fits, up to
 * `maxZoom`, centred in the part of a `canvas`-sized pane that `reserve`
 * leaves free. React Flow draws world point p at `p * zoom + (x, y)`.
 */
export function viewportForFocus(
  box: Rect,
  canvas: { width: number; height: number },
  reserve: Reserve,
  { padding = 32, maxZoom = 1, minZoom = 0.3 }: { padding?: number; maxZoom?: number; minZoom?: number } = {},
): Viewport {
  const freeWidth = Math.max(1, canvas.width - reserve.left - reserve.right);
  const freeHeight = Math.max(1, canvas.height - reserve.top - reserve.bottom);
  const zoom = Math.max(
    minZoom,
    Math.min(maxZoom, freeWidth / (box.width + padding * 2), freeHeight / (box.height + padding * 2)),
  );
  const centreX = reserve.left + freeWidth / 2;
  const centreY = reserve.top + freeHeight / 2;
  return {
    x: centreX - (box.x + box.width / 2) * zoom,
    y: centreY - (box.y + box.height / 2) * zoom,
    zoom,
  };
}
