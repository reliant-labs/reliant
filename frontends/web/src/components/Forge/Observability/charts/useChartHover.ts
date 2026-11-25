// Copyright (c) 2026 Reliant Labs

/**
 * The stateful half of a chart's hover read-out (chartHover.ts is the pure
 * half): which sample is highlighted, and how the mouse, a finger and the
 * keyboard move it.
 *
 *   mouse     hover highlights the sample under the pointer; leaving clears it.
 *             A chart that tracks the mouse itself (uPlot's cursor) reports it
 *             through `show` and sets `nativeMouse`.
 *   touch     a tap, or a horizontal drag, moves the highlight, and it stays
 *             after the finger lifts. The plot is `touch-action: pan-y`, so a
 *             vertical swipe still scrolls the page. A tap elsewhere clears
 *             it, and so does scrolling the chart out of view.
 *   keyboard  the chart is focusable: ArrowLeft / ArrowRight step through the
 *             samples, Home / End jump to the ends, Escape (or leaving the
 *             chart) clears. A keyboard highlight is announced (`announce`).
 */

import { useCallback, useEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent, type RefObject } from "react";

import { stepSample, type SampleAnchor } from "./chartHover";

/** What set the highlight: the mouse (`pointer`), a finger or pen (`touch`), or the keys. */
export type HoverVia = "pointer" | "touch" | "keyboard";

export interface ChartHover {
  index: number;
  via: HoverVia;
  /** Where the sample sits on screen; null when the chart cannot say (no layout). */
  anchor: SampleAnchor | null;
}

export interface UseChartHoverOptions {
  /** The chart's root: a pointer-down outside it clears a touch highlight. */
  rootRef: RefObject<Element | null>;
  length: number;
  /** Whether a sample has a value in any series: the arrow keys skip the ones that do not. */
  hasSample: (index: number) => boolean;
  /** Where sample `index` sits, in viewport pixels. */
  anchorOf: (index: number) => SampleAnchor | null;
  /** The sample under a viewport x, or null outside the plot. */
  indexAt: (clientX: number) => number | null;
  /** The keyboard or a finger moved the highlight (null: cleared): a chart with its own cursor mirrors it. */
  onSelect?: (index: number | null) => void;
  /** Whether the chart is tracking the mouse itself right now (uPlot's cursor), reporting it through `show`. */
  nativeMouse?: () => boolean;
}

export interface UseChartHoverResult {
  hover: ChartHover | null;
  /** Highlight `index` (null clears), as the pointer or the keyboard. */
  show: (index: number | null, via: HoverVia) => void;
  /** Spread on the focusable element that holds the plot. */
  bind: {
    tabIndex: 0;
    onKeyDown: (event: KeyboardEvent) => void;
    onBlur: () => void;
    onPointerDown: (event: PointerEvent) => void;
    onPointerMove: (event: PointerEvent) => void;
    onPointerLeave: (event: PointerEvent) => void;
  };
  /** For the plot itself: horizontal drags are the chart's, vertical ones scroll the page. */
  plotStyle: CSSProperties;
}

const PLOT_STYLE: CSSProperties = { touchAction: "pan-y" };

export function useChartHover({ rootRef, length, hasSample, anchorOf, indexAt, onSelect, nativeMouse }: UseChartHoverOptions): UseChartHoverResult {
  const [hover, setHover] = useState<ChartHover | null>(null);
  const latest = useRef({ hover, length, hasSample, anchorOf, indexAt, onSelect, nativeMouse });
  latest.current = { hover, length, hasSample, anchorOf, indexAt, onSelect, nativeMouse };

  const show = useCallback((index: number | null, via: HoverVia) => {
    setHover((prev) => {
      if (index === null || index < 0 || index >= latest.current.length) {
        // The mouse leaving does not clear a highlight the keys or a finger set:
        // after a tap the browser may report the mouse back where it was, off the chart.
        return prev && via === "pointer" && prev.via !== "pointer" ? prev : null;
      }
      // A mouse report of the sample the keys or a finger just chose (the chart's cursor echoing it) keeps it theirs.
      if (prev && prev.index === index && prev.via !== "pointer" && via === "pointer") return prev;
      if (prev && prev.index === index && prev.via === via) return prev;
      return { index, via, anchor: latest.current.anchorOf(index) };
    });
  }, []);

  // Re-read the anchor after a highlight lands: the chart may only now have laid out the cursor.
  useEffect(() => {
    if (!hover) return;
    const anchor = latest.current.anchorOf(hover.index);
    if (anchor && (!hover.anchor || anchor.x !== hover.anchor.x || anchor.top !== hover.anchor.top || anchor.bottom !== hover.anchor.bottom)) {
      setHover((prev) => (prev && prev.index === hover.index ? { ...prev, anchor } : prev));
    }
  }, [hover]);

  const select = useCallback(
    (index: number | null, via: HoverVia) => {
      latest.current.onSelect?.(index);
      show(index, via);
    },
    [show]
  );

  // A highlight left by a finger stays until a tap elsewhere. On a scroll the
  // tooltip (fixed to the viewport) follows its sample, and goes once the
  // chart has scrolled out of view: a scroll elsewhere on the page, or the one
  // that brings a tapped chart into view, does not take it away.
  const active = hover !== null;
  useEffect(() => {
    if (!active) return;
    const outside = (event: Event) => {
      const root = rootRef.current;
      if (root && event.target instanceof Node && root.contains(event.target)) return;
      select(null, "keyboard");
    };
    const scrolled = () => {
      const current = latest.current.hover;
      if (!current) return;
      const anchor = latest.current.anchorOf(current.index);
      if (!anchor || anchor.bottom < 0 || anchor.top > window.innerHeight) {
        select(null, "keyboard");
        return;
      }
      setHover((prev) => (prev && prev.index === current.index ? { ...prev, anchor } : prev));
    };
    document.addEventListener("pointerdown", outside, true);
    window.addEventListener("scroll", scrolled, true);
    return () => {
      document.removeEventListener("pointerdown", outside, true);
      window.removeEventListener("scroll", scrolled, true);
    };
  }, [active, rootRef, select]);

  const onKeyDown = useCallback(
    (event: KeyboardEvent) => {
      const { hover: current, length: n, hasSample: has } = latest.current;
      const next = stepSample(current?.index ?? null, event.key, n, has);
      if (next === undefined) return;
      // Escape with nothing highlighted belongs to whatever is behind the chart (a drawer, a dialog).
      if (next === null && !current) return;
      event.preventDefault();
      event.stopPropagation();
      select(next, "keyboard");
    },
    [select]
  );

  const fromPointer = useCallback(
    (event: PointerEvent) => {
      if (event.pointerType === "mouse" && latest.current.nativeMouse?.()) return;
      const index = latest.current.indexAt(event.clientX);
      if (index === null) return;
      if (event.pointerType === "mouse") show(index, "pointer");
      else select(index, "touch");
    },
    [select, show]
  );

  const onPointerLeave = useCallback(
    (event: PointerEvent) => {
      // A finger's highlight outlives the finger; the mouse's does not.
      if (event.pointerType === "mouse" && !latest.current.nativeMouse?.()) show(null, "pointer");
    },
    [show]
  );

  const onBlur = useCallback(() => {
    if (latest.current.hover?.via === "keyboard") select(null, "keyboard");
  }, [select]);

  return {
    hover,
    show,
    bind: { tabIndex: 0, onKeyDown, onBlur, onPointerDown: fromPointer, onPointerMove: fromPointer, onPointerLeave },
    plotStyle: PLOT_STYLE,
  };
}
