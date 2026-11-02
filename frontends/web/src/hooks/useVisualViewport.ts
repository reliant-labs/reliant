import { useEffect, useState } from "react";
import { measureVisualViewport } from "../lib/responsiveGeometry";

export interface VisualViewportMetrics {
  /** Height of the area the user can actually see (shrinks when the keyboard opens). */
  height: number;
  /** How far the browser has panned the visual viewport down the layout viewport. */
  offsetTop: number;
  /** True while an on-screen keyboard covers the bottom of the layout viewport. */
  keyboardOpen: boolean;
}

function read(vv: VisualViewport): VisualViewportMetrics {
  return measureVisualViewport(vv, window.innerHeight);
}

/**
 * The visual viewport's size and pan offset, or null where the browser has no
 * `visualViewport` (jsdom, old browsers) and `100dvh` is all there is.
 *
 * `dvh` follows the URL bar but NOT the on-screen keyboard: iOS Safari and
 * Chrome Android both leave the layout viewport full height when the keyboard
 * opens and only shrink the visual one, so a `100dvh` shell keeps a composer
 * underneath the keyboard. Anything that has to fit "what is on screen" reads
 * this instead. Pass `enabled=false` to opt out without subscribing.
 */
export function useVisualViewport(enabled = true): VisualViewportMetrics | null {
  const [metrics, setMetrics] = useState<VisualViewportMetrics | null>(() =>
    enabled && typeof window !== "undefined" && window.visualViewport
      ? read(window.visualViewport)
      : null,
  );

  useEffect(() => {
    const vv = window.visualViewport;
    if (!enabled || !vv) return;
    const update = () =>
      setMetrics((prev) => {
        const next = read(vv);
        return prev &&
          prev.height === next.height &&
          prev.offsetTop === next.offsetTop &&
          prev.keyboardOpen === next.keyboardOpen
          ? prev
          : next;
      });
    update();
    vv.addEventListener("resize", update);
    vv.addEventListener("scroll", update);
    // Rotation/split-view can update innerHeight after the visual viewport
    // event. Re-read both instead of retaining a stale keyboard classification.
    window.addEventListener("resize", update);
    return () => {
      vv.removeEventListener("resize", update);
      vv.removeEventListener("scroll", update);
      window.removeEventListener("resize", update);
    };
  }, [enabled]);

  return enabled ? metrics : null;
}
