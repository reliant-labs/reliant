/**
 * The workflows journey's pieces that live on the builder canvas:
 *
 *  - `OnCanvas` pans the canvas to the steps a card explains and draws an
 *    invisible box around them (`[data-tour='wf-focus']`). The step targets
 *    that box, so the engine places the card beside all of them, never over
 *    one, and the spotlight follows the box when the canvas moves.
 *  - `DragDemo` shows how an edge is drawn: a ghost cursor presses the dot on
 *    the right of one step, drags to the next, the edge draws itself, then
 *    goes. It loops; under prefers-reduced-motion it is a still diagram.
 *  - `TryItYourself` lends the user that edge to draw (optional).
 *
 * The builder side is canvas/tourCanvas.ts and hooks/useTourCanvas.ts.
 */

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { tourCanvas, unionRect, type Rect, type Reserve } from "../../workflow/canvas/tourCanvas";
import { tourEvents } from "../tourEvents";

/** The focus box's selector: what a canvas step targets. */
export const FOCUS_TARGET = "[data-tour='wf-focus']";

/** Screen pixels the card needs beside the focus box: its width (20rem), the gap and the margin. */
export const CARD_ROOM = 20 * 16 + 16 + 16;

/**
 * What to keep clear when panning: the builder's floating step palette on the
 * left, its toolbar at the bottom, and room for the card on the right.
 */
export const CANVAS_RESERVE: Reserve = { top: 24, right: CARD_ROOM + 24, bottom: 96, left: 264 };

export const nodeSelector = (id: string) => `.react-flow__node[data-id="${id}"]`;

export function prefersReducedMotion(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia("(prefers-reduced-motion: reduce)").matches
    : false;
}

function screenRect(selector: string): Rect | null {
  const el = document.querySelector(selector);
  if (!el) return null;
  const r = el.getBoundingClientRect();
  return r.width > 0 && r.height > 0 ? { x: r.left, y: r.top, width: r.width, height: r.height } : null;
}

/** Calls `onFrame` every animation frame while mounted. */
function useEveryFrame(onFrame: (now: number) => void, active = true) {
  const latest = useRef(onFrame);
  latest.current = onFrame;
  useEffect(() => {
    if (!active) return;
    let frame = 0;
    const tick = (now: number) => {
      latest.current(now);
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [active]);
}

const same = (a: Rect | null, b: Rect | null) =>
  a === b ||
  (!!a && !!b && Math.abs(a.x - b.x) < 0.5 && Math.abs(a.y - b.y) < 0.5 && Math.abs(a.width - b.width) < 0.5 && Math.abs(a.height - b.height) < 0.5);

/**
 * Pans to `nodes` and boxes `box` (default: those nodes) for the spotlight.
 * Renders nothing in the card itself.
 */
export function OnCanvas({ nodes, box, padding = 6 }: { nodes: string[]; box?: string[]; padding?: number }) {
  const selectors = box ?? nodes.map(nodeSelector);
  const key = nodes.join(",");
  const [rect, setRect] = useState<Rect | null>(null);
  const el = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    tourCanvas.focus({ nodeIds: nodes, reserve: CANVAS_RESERVE, animate: !prefersReducedMotion() });
    return () => tourCanvas.release();
    // `nodes` is static step content; `key` names it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  useEveryFrame(() => {
    const rects = selectors.map(screenRect).filter((r): r is Rect => !!r);
    const next = rects.length === selectors.length ? unionRect(rects) : null;
    const padded = next && { x: next.x - padding, y: next.y - padding, width: next.width + padding * 2, height: next.height + padding * 2 };
    setRect((prev) => (same(prev, padded) ? prev : padded));
  });

  // The spotlight re-measures its target on scroll (capture, on window): say the box moved.
  useLayoutEffect(() => {
    el.current?.dispatchEvent(new Event("scroll"));
  }, [rect]);

  if (!rect) return null;
  return createPortal(
    <div
      ref={el}
      aria-hidden="true"
      data-tour="wf-focus"
      data-testid="wf-focus"
      className="pointer-events-none fixed"
      style={{ top: rect.y, left: rect.x, width: rect.width, height: rect.height, borderRadius: 12 }}
    />,
    document.body,
  );
}

// ─── The drag demonstration ──────────────────────────────────────────────────

interface Point {
  x: number;
  y: number;
}

function centre(selector: string): Point | null {
  const r = screenRect(selector);
  return r ? { x: r.x + r.width / 2, y: r.y + r.height / 2 } : null;
}

/** The curve React Flow draws between a source on the right and a target on the left. */
export function edgePath(from: Point, to: Point): string {
  const pull = Math.max(Math.abs(to.x - from.x) / 2, 40);
  return `M ${from.x} ${from.y} C ${from.x + pull} ${from.y} ${to.x - pull} ${to.y} ${to.x} ${to.y}`;
}

/** One loop of the demonstration, in ms, and its beats. */
export const DEMO_CYCLE = 3600;
const PRESS = 500;
const DRAG_END = 2000;
const LAND_END = 2500;
const FADE_END = 3100;

const ease = (t: number) => (t < 0.5 ? 2 * t * t : 1 - (-2 * t + 2) ** 2 / 2);

/** Where the demonstration is at `t` ms into a loop: pure, so the beats are testable. */
export function demoFrame(t: number): { cursor: number; drawn: number; solid: boolean; ghost: number; hideReal: boolean; pressed: boolean } {
  const at = ((t % DEMO_CYCLE) + DEMO_CYCLE) % DEMO_CYCLE;
  const drag = Math.min(1, Math.max(0, (at - PRESS) / (DRAG_END - PRESS)));
  const p = ease(drag);
  return {
    cursor: at < 300 ? at / 300 : at < LAND_END ? 1 : Math.max(0, 1 - (at - LAND_END) / 300),
    drawn: at < PRESS ? 0 : p,
    solid: at >= DRAG_END,
    ghost: at < LAND_END ? 1 : Math.max(0, 1 - (at - LAND_END) / (FADE_END - LAND_END)),
    hideReal: at >= PRESS - 100 && at < FADE_END,
    pressed: at >= 250 && at < DRAG_END,
  };
}

const CURSOR_PATH = "M0 0 L0 15 L4 11 L7 18 L10 17 L7 10 L12 10 Z";

/**
 * Drag from `source`'s output dot to `target`'s input dot, on the real canvas.
 * `paused` hides it (the user is trying it themselves).
 */
export function DragDemo({ source, target, paused = false }: { source: string; target: string; paused?: boolean }) {
  const reduced = prefersReducedMotion();
  const [ends, setEnds] = useState<{ from: Point; to: Point } | null>(null);
  const pathRef = useRef<SVGPathElement | null>(null);
  const cursorRef = useRef<SVGGElement | null>(null);
  const arrowRef = useRef<SVGPathElement | null>(null);
  const ringRef = useRef<SVGCircleElement | null>(null);
  const groupRef = useRef<SVGGElement | null>(null);
  const start = useRef<number | null>(null);
  const realEdge = `.react-flow__edge[aria-label="Edge from ${source} to ${target}"]`;

  useEveryFrame((now) => {
    const from = centre(`.react-flow__handle.source[data-nodeid="${source}"]`);
    const to = centre(`.react-flow__handle.target[data-nodeid="${target}"]`);
    const next = from && to ? { from, to } : null;
    setEnds((prev) =>
      prev && next && Math.abs(prev.from.x - next.from.x) < 0.5 && Math.abs(prev.from.y - next.from.y) < 0.5 && Math.abs(prev.to.x - next.to.x) < 0.5 && Math.abs(prev.to.y - next.to.y) < 0.5
        ? prev
        : next,
    );
    if (reduced || paused) return;
    start.current ??= now;
    const frame = demoFrame(now - start.current);
    const path = pathRef.current;
    if (!path || !cursorRef.current || !groupRef.current) return;
    const length = typeof path.getTotalLength === "function" ? path.getTotalLength() : 0;
    path.style.strokeDasharray = frame.solid ? "none" : `${length}`;
    path.style.strokeDashoffset = frame.solid ? "0" : `${length * (1 - frame.drawn)}`;
    path.style.stroke = frame.solid ? "hsl(var(--primary))" : "hsl(var(--primary) / 0.8)";
    groupRef.current.style.opacity = String(frame.ghost);
    if (arrowRef.current) arrowRef.current.style.opacity = frame.solid ? "1" : "0";
    if (ringRef.current) ringRef.current.style.opacity = frame.pressed ? "1" : "0";
    if (length > 0) {
      const point = path.getPointAtLength(length * frame.drawn);
      cursorRef.current.setAttribute("transform", `translate(${point.x} ${point.y}) scale(${frame.pressed ? 1.3 : 1.45})`);
    }
    cursorRef.current.style.opacity = String(frame.cursor);
    const real = document.querySelector<SVGElement>(realEdge);
    if (real) real.style.opacity = frame.hideReal ? "0.08" : "";
  });

  // Leave the real edge as it was.
  useEffect(
    () => () => {
      const real = document.querySelector<SVGElement>(realEdge);
      if (real) real.style.opacity = "";
    },
    [realEdge],
  );
  useEffect(() => {
    if (!paused) return;
    start.current = null;
    const real = document.querySelector<SVGElement>(realEdge);
    if (real) real.style.opacity = "";
  }, [paused, realEdge]);

  if (!ends || paused) return null;
  const d = edgePath(ends.from, ends.to);
  const { to, from } = ends;
  const mid = { x: (from.x + to.x) / 2, y: (from.y + to.y) / 2 };
  return createPortal(
    <svg
      aria-hidden="true"
      data-testid="wf-drag-demo"
      data-motion={reduced ? "static" : "animated"}
      className="pointer-events-none fixed inset-0 z-[100] h-full w-full overflow-visible"
    >
      <defs>
        <marker id="wf-demo-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M 0 0 L 10 5 L 0 10 z" fill="hsl(var(--primary))" />
        </marker>
      </defs>
      {reduced ? (
        // Still: the drag as a diagram. Dashed path, the arrow, a cursor on the way and its label.
        <g data-testid="wf-drag-demo-static">
          <circle cx={from.x} cy={from.y} r={9} fill="none" stroke="hsl(var(--primary))" strokeWidth={2} />
          <path d={d} fill="none" stroke="hsl(var(--primary))" strokeWidth={2.5} strokeDasharray="6 5" markerEnd="url(#wf-demo-arrow)" />
          <g transform={`translate(${mid.x} ${mid.y}) scale(1.3)`}>
            <path d={CURSOR_PATH} fill="hsl(var(--foreground))" stroke="hsl(var(--background))" strokeWidth={1.2} />
            <text x={16} y={-4} fontSize={12} fontWeight={600} fill="hsl(var(--primary))">
              drag
            </text>
          </g>
        </g>
      ) : (
        <g ref={groupRef}>
          <circle ref={ringRef} cx={from.x} cy={from.y} r={10} fill="hsl(var(--primary) / 0.2)" stroke="hsl(var(--primary))" strokeWidth={2} style={{ opacity: 0 }} />
          <path ref={pathRef} d={d} fill="none" strokeWidth={3} strokeLinecap="round" />
          <path ref={arrowRef} d={d} fill="none" stroke="transparent" markerEnd="url(#wf-demo-arrow)" style={{ opacity: 0 }} />
          <g ref={cursorRef} transform={`translate(${from.x} ${from.y})`} style={{ opacity: 0 }}>
            <path d={CURSOR_PATH} fill="hsl(var(--foreground))" stroke="hsl(var(--background))" strokeWidth={1.2} />
          </g>
        </g>
      )}
    </svg>,
    document.body,
  );
}

// ─── Try it yourself ─────────────────────────────────────────────────────────

/**
 * The edge step's demonstration and its optional exercise. "Try it yourself"
 * takes the edge out (unsaved) for the user to draw; drawing it completes the
 * step. Leaving the step without drawing it puts it back.
 */
export function EdgeLesson({ source, target }: { source: string; target: string }) {
  const [trying, setTrying] = useState(false);
  const drawn = useRef(false);

  useEffect(
    () =>
      tourEvents.on("workflow:edgeConnected", (p) => {
        if (p.sourceId === source && p.targetId === target) drawn.current = true;
      }),
    [source, target],
  );
  // Leaving mid-try: put the edge back.
  useEffect(
    () => () => {
      if (!drawn.current) tourCanvas.reattachEdge();
    },
    [],
  );

  return (
    <>
      <DragDemo source={source} target={target} paused={trying} />
      {trying ? (
        <p className="mt-2 text-xs text-foreground" data-testid="wf-try-instructions">
          Now you: drag from the dot on the right of <code>{source}</code> to the dot on the left of <code>{target}</code>.{" "}
          <button
            type="button"
            className="font-medium text-muted-foreground underline underline-offset-2 hover:text-foreground"
            onClick={() => {
              tourCanvas.reattachEdge();
              setTrying(false);
            }}
          >
            Put it back
          </button>
        </p>
      ) : (
        <div className="mt-3 flex items-center gap-2">
          <button
            type="button"
            data-testid="wf-try-edge"
            className="rounded-md border border-border px-2.5 py-1.5 text-xs font-medium text-foreground hover:bg-muted"
            onClick={() => {
              tourCanvas.detachEdge(source, target);
              setTrying(true);
            }}
          >
            Try it yourself
          </button>
          <span className="text-xs text-muted-foreground">Optional</span>
        </div>
      )}
    </>
  );
}
