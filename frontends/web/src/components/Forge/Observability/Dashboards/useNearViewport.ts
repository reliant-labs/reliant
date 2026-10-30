// Copyright (c) 2025 Reliant Labs

import { useCallback, useEffect, useRef, useState } from "react";

/** How far outside the viewport a panel still counts as near: about a screen, so it has loaded by the time it scrolls in. */
export const NEAR_VIEWPORT_MARGIN = "600px";

/**
 * True once the element has come within NEAR_VIEWPORT_MARGIN of the viewport,
 * and true from then on (a panel that has loaded keeps polling; scrolling away
 * does not unload it). A dashboard reads only the panels near the viewport, so
 * the twenty below the fold do not queue behind the ones being looked at.
 * Without IntersectionObserver every element is near.
 */
export function useNearViewport(margin: string = NEAR_VIEWPORT_MARGIN): [ref: (element: Element | null) => void, near: boolean] {
  const supported = typeof IntersectionObserver !== "undefined";
  const [near, setNear] = useState(!supported);
  const observer = useRef<IntersectionObserver | null>(null);

  const ref = useCallback(
    (element: Element | null) => {
      observer.current?.disconnect();
      observer.current = null;
      if (!element || !supported) return;
      const io = new IntersectionObserver(
        (entries) => {
          if (entries.some((entry) => entry.isIntersecting)) {
            setNear(true);
            io.disconnect();
          }
        },
        { rootMargin: margin }
      );
      io.observe(element);
      observer.current = io;
    },
    [margin, supported]
  );

  useEffect(() => () => observer.current?.disconnect(), []);
  return [ref, near];
}
