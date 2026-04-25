/**
 * OverscrollBounce — Wraps around Virtuoso to provide elastic overscroll.
 *
 * Applies a CSS transform on a div OUTSIDE Virtuoso so Virtuoso's internal
 * layout, resize observers, and scroll calculations are completely undisturbed.
 *
 * Checks boundary state by reading scrollTop/scrollHeight from the Virtuoso
 * scroller element passed via scrollerElRef.
 */

import React, { useRef, useCallback, useEffect } from "react";

const MAX_DISPLACEMENT = 60;
const RUBBER_BAND_COEFF = 0.55;
const DECAY = 0.6;
const SNAP_THRESHOLD = 0.5;

interface OverscrollBounceProps {
  children: React.ReactNode;
  scrollerElRef: React.MutableRefObject<HTMLDivElement | null>;
  isStreaming?: boolean;
}

function rubberBand(raw: number): number {
  const d = MAX_DISPLACEMENT;
  const c = RUBBER_BAND_COEFF;
  const abs = Math.abs(raw);
  const displaced = (1 - 1 / ((abs * c) / d + 1)) * d;
  return raw < 0 ? -displaced : displaced;
}

export function OverscrollBounce({ children, scrollerElRef, isStreaming }: OverscrollBounceProps) {
  const wrapperRef = useRef<HTMLDivElement>(null);
  const displacementRef = useRef(0);
  const excessRef = useRef(0);
  const rafRef = useRef<number | null>(null);
  const activeRef = useRef(false);
  const streamingRef = useRef(false);
  streamingRef.current = !!isStreaming;

  const applyTransform = useCallback((px: number) => {
    displacementRef.current = px;
    const el = wrapperRef.current;
    if (!el) return;
    if (px === 0) {
      el.style.transform = "";
    } else {
      el.style.transform = `translate3d(0,${px}px,0)`;
    }
  }, []);

  const stopLoop = useCallback(() => {
    if (rafRef.current !== null) {
      cancelAnimationFrame(rafRef.current);
      rafRef.current = null;
    }
  }, []);

  const startLoop = useCallback(() => {
    if (rafRef.current !== null) return;

    const tick = () => {
      // Decay the visual displacement directly
      const d = displacementRef.current * DECAY;

      if (Math.abs(d) < SNAP_THRESHOLD) {
        excessRef.current = 0;
        applyTransform(0);
        activeRef.current = false;
        rafRef.current = null;
        return;
      }

      applyTransform(d);
      rafRef.current = requestAnimationFrame(tick);
    };

    rafRef.current = requestAnimationFrame(tick);
  }, [applyTransform]);

  useEffect(() => {
    const wrapper = wrapperRef.current;
    if (!wrapper) return;

    const onWheel = (e: WheelEvent) => {
      if (e.deltaMode !== 0) return;
      const scroller = scrollerElRef.current;
      if (!scroller) return;

      const dy = e.deltaY;

      if (activeRef.current) {
        const d = displacementRef.current;
        // Scrolling back toward content → cancel
        if ((d > 0 && dy > 0) || (d < 0 && dy < 0)) {
          stopLoop();
          excessRef.current = 0;
          applyTransform(0);
          activeRef.current = false;
          return;
        }

        // Same direction — accumulate
        excessRef.current += dy;
        applyTransform(rubberBand(-excessRef.current));
        e.preventDefault();
        return;
      }

      const { scrollTop, scrollHeight, clientHeight } = scroller;
      const atTop = scrollTop <= 0;
      const atBottom = scrollTop + clientHeight >= scrollHeight - 1;

      if (atTop && dy < 0) {
        activeRef.current = true;
        excessRef.current = dy;
        applyTransform(rubberBand(-excessRef.current));
        startLoop();
        e.preventDefault();
        return;
      }

      if (atBottom && dy > 0 && !streamingRef.current) {
        activeRef.current = true;
        excessRef.current = dy;
        applyTransform(rubberBand(-excessRef.current));
        startLoop();
        e.preventDefault();
        return;
      }
    };

    wrapper.addEventListener("wheel", onWheel, { passive: false });
    return () => {
      wrapper.removeEventListener("wheel", onWheel);
      stopLoop();
    };
  }, [scrollerElRef, applyTransform, startLoop, stopLoop]);

  return (
    <div ref={wrapperRef} style={{ height: "100%", position: "relative" }}>
      {children}
    </div>
  );
}
