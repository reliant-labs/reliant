/**
 * Virtualization and scroll-follow for the chat transcript.
 *
 * The transcript must do three things at once, and every earlier attempt got
 * one of them wrong:
 *
 *   1. Render only what is near the viewport — a long chat is thousands of
 *      rows, and mounting all of them stalls the main thread.
 *   2. Stay pinned to the bottom while the newest row streams, WITHOUT a
 *      visible lurch.
 *   3. Hold the reader's place when they have scrolled up — including when
 *      rows above them are measured, grow, or older pages are prepended.
 *
 * react-virtuoso did 1 and 3, but corrected 2 a frame late: it only scrolled
 * after the bottom drifted past `atBottomThreshold`, and issued that scroll
 * from requestAnimationFrame — so every streamed delta painted below the fold
 * first and snapped into view after. That staircase WAS the jitter: measured
 * on the real timeline, every painted frame of a stream landed off the bottom
 * (worst 186px for plain text, 644px when tool/diff cards expanded mid-turn).
 *
 * @tanstack/virtual-core with `anchorTo: "end"` does all three, and corrects
 * in the right place: row resizes are measured in a ResizeObserver and the
 * scroll adjustment is applied SYNCHRONOUSLY in that callback — after layout,
 * before paint — so the frame that paints the growth already paints it at the
 * bottom. It also compensates rows measured or resized ABOVE the viewport
 * (holding a scrolled-up reader still, including in WebKit, which has no CSS
 * scroll anchoring), deliberately leaves alone a row that merely spans the
 * fold (the streaming message), and defers corrections during iOS momentum.
 *
 * So the virtualizer is the ONLY writer of scrollTop. The footer (thinking
 * indicator, approval prompt) is not a row, but its measured height is fed in
 * as `paddingEnd`, so "the end" the virtualizer pins to includes it.
 *
 * Following is position-based: within END_THRESHOLD_PX of the end means
 * following. Scrolling up stops it; reaching the bottom resumes it. A held
 * primary-button press in the transcript suspends it until release, so a
 * drag-selection is never scrolled under a still cursor.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useVirtualizer, type ReactVirtualizer } from "@tanstack/react-virtual";

/** Within this many px of the end counts as "at the bottom" (following). */
export const END_THRESHOLD_PX = 4;

/**
 * While a press is held, no position counts as "at the end": the
 * virtualizer's at-end checks compare a non-negative distance against this.
 */
const SUSPENDED_THRESHOLD_PX = -1;

/** Starting height guess for an unmeasured row; corrected on first measure. */
const ESTIMATED_ROW_PX = 160;

/**
 * Rows rendered beyond the viewport each way.
 *
 * Measured, not guessed (2000 messages, wheel-scrolling up through history):
 * 2–3 rows beat react-virtuoso on every smoothness number; 6 fell
 * back to Virtuoso's level; 8 was clearly worse (p99 175ms, 227ms stalls),
 * because each wheel step mounts that many more markdown rows. Text selection
 * does not need a larger window: rows are measured and positioned, a held
 * press suspends following, and a re-render no longer rebuilds a row's DOM.
 */
const OVERSCAN_ROWS = 3;

export interface TimelineVirtualizer {
  /** The React adapter's virtualizer: its containerRef sizes the virtual space. */
  virtualizer: ReactVirtualizer<HTMLDivElement, HTMLDivElement>;
  /** Ref for the element with `overflow-y: auto`. */
  scrollerRef: (element: HTMLDivElement | null) => void;
  /**
   * Ref for the footer. Render it inside the virtual container, absolutely
   * positioned at `bottom: 0`; its height is measured and reserved as
   * paddingEnd, so the virtualizer's end includes it.
   */
  footerRef: (element: HTMLDivElement | null) => void;
  /** The scroller element, once mounted. */
  scroller: HTMLDivElement | null;
  /** Whether the viewport is at the bottom (drives the scroll-to-bottom button). */
  atBottom: boolean;
  /** Jump to the very bottom (footer included); following resumes there. */
  scrollToBottom: () => void;
}

function distanceFromEnd(element: HTMLElement): number {
  return element.scrollHeight - element.clientHeight - element.scrollTop;
}

export function useTimelineVirtualizer(params: {
  count: number;
  getItemKey: (index: number) => string;
  /** Space reserved above the first row (the scroll-back loading header). */
  paddingStart: number;
  /**
   * Height of the overlay floating over the top of the viewport (the pinned
   * user-message header). A jump that aligns a row to the start lands it
   * below the overlay instead of underneath it.
   */
  scrollPaddingStart: number;
}): TimelineVirtualizer {
  const { count, getItemKey, paddingStart, scrollPaddingStart } = params;
  const [scroller, setScroller] = useState<HTMLDivElement | null>(null);
  const [footer, setFooter] = useState<HTMLDivElement | null>(null);
  const [footerHeight, setFooterHeight] = useState(0);
  const [atBottom, setAtBottom] = useState(true);
  const atBottomRef = useRef(true);
  // State, not a ref: the threshold is an option the adapter re-applies on
  // every render, so the suspension has to be part of what we render with.
  const [pressHeld, setPressHeld] = useState(false);
  const pressHeldRef = useRef(false);
  pressHeldRef.current = pressHeld;

  const virtualizer = useVirtualizer<HTMLDivElement, HTMLDivElement>({
    count,
    getScrollElement: () => scroller,
    estimateSize: () => ESTIMATED_ROW_PX,
    getItemKey,
    overscan: OVERSCAN_ROWS,
    paddingStart,
    scrollPaddingStart,
    // The footer lives in this reserved space, so the end includes it.
    paddingEnd: footerHeight,
    // Bottom-anchored: growth at the end while at the end keeps the end in
    // view; edits above the viewport keep the reader's row still; a prepend
    // (an older page) holds position.
    anchorTo: "end",
    // A NEW row arriving while at the end stays in view. Instant, never
    // smooth: a smooth scroll is retargeted by every streamed delta and reads
    // as rubber-banding.
    followOnAppend: "auto",
    scrollEndThreshold: pressHeld ? SUSPENDED_THRESHOLD_PX : END_THRESHOLD_PX,
    // Corrections in the ResizeObserver callback itself (pre-paint), not a
    // frame later. This is the library default; stated because it IS the fix.
    useAnimationFrameWithResizeObserver: false,
    // Scroll-only updates write row transforms and the container height
    // straight to the DOM; React re-renders the transcript only when the
    // RANGE of rendered rows changes. Without this, every scroll event
    // re-rendered every visible row (markdown included) — measured worst
    // stalls of ~230ms at 2000 messages versus Virtuoso's ~80.
    // Rows must therefore not set `transform`, and the container must not set
    // `height`, in JSX: the virtualizer owns both.
    directDomUpdates: true,
    directDomUpdatesMode: "transform",
    onChange: (instance) => {
      const element = instance.scrollElement;
      if (!element) return;
      // setState only on a CHANGE: onChange fires on every scroll event, and
      // an unconditional setState would re-render the transcript on each one,
      // undoing directDomUpdates.
      const next = distanceFromEnd(element) <= END_THRESHOLD_PX;
      if (next !== atBottomRef.current) {
        atBottomRef.current = next;
        setAtBottom(next);
      }
    },
  });

  // Open at the bottom, through the virtualizer: scrollToEnd() targets the
  // last row and then RECONCILES across frames as rows are measured and turn
  // out taller than the estimate, until the position holds. A direct
  // scrollTop write here would land short (rows still at their estimates) and
  // leave the virtualizer's own offset stale, so it would never count as "at
  // the end" and would not follow — the timeline opened ~800px short and
  // stayed there. Layout effect, so the first paint is already near the end.
  const openedRef = useRef(false);
  useLayoutEffect(() => {
    if (!scroller || openedRef.current || count === 0) return;
    openedRef.current = true;
    virtualizer.scrollToEnd({ behavior: "auto" });
  }, [scroller, count, virtualizer]);

  // A held press suspends following; release restores it.
  useEffect(() => {
    if (!scroller) return;
    const onPointerDown = (event: PointerEvent) => {
      // Primary button only — a right-click for the context menu is not a
      // read. The scroller itself is its scrollbar: grabbing it is a scroll.
      if (event.button !== 0 || event.target === scroller) return;
      setPressHeld(true);
    };
    const onPointerUp = () => {
      if (pressHeldRef.current) setPressHeld(false);
    };
    scroller.addEventListener("pointerdown", onPointerDown);
    // On the window: the release can land anywhere, including outside the app.
    window.addEventListener("pointerup", onPointerUp);
    window.addEventListener("pointercancel", onPointerUp);
    return () => {
      scroller.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("pointerup", onPointerUp);
      window.removeEventListener("pointercancel", onPointerUp);
    };
  }, [scroller]);

  // Measure the footer and reserve its height as paddingEnd. When it grows
  // while at the end (the thinking indicator appearing), the virtualizer's
  // total grows by the same amount and its end-anchoring keeps the end in
  // view. The measurement lands in a ResizeObserver callback; the re-render
  // it causes applies the new paddingEnd before the next paint.
  useLayoutEffect(() => {
    if (!footer) return;
    const measure = () => setFooterHeight(Math.round(footer.getBoundingClientRect().height));
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(footer);
    return () => observer.disconnect();
  }, [footer]);

  // paddingEnd changed while at the end: the end moved, so follow it. Layout
  // effect, so the correction lands before paint.
  const previousFooterHeightRef = useRef(footerHeight);
  useLayoutEffect(() => {
    const grew = footerHeight - previousFooterHeightRef.current;
    previousFooterHeightRef.current = footerHeight;
    if (!scroller || grew <= 0 || pressHeldRef.current) return;
    // At the end BEFORE this growth means the gap now equals the growth.
    if (distanceFromEnd(scroller) - grew <= END_THRESHOLD_PX) {
      virtualizer.scrollToEnd({ behavior: "auto" });
    }
  }, [footerHeight, scroller, virtualizer]);

  const scrollToBottom = useCallback(() => {
    if (count > 0) virtualizer.scrollToEnd({ behavior: "auto" });
  }, [count, virtualizer]);

  return {
    virtualizer,
    scrollerRef: setScroller,
    footerRef: setFooter,
    scroller,
    atBottom,
    scrollToBottom,
  };
}
