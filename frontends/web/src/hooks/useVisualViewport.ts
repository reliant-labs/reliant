import { useEffect, useState } from "react";
import { isEditableElement, measureVisualViewport } from "../lib/responsiveGeometry";

export interface VisualViewportMetrics {
  /** Height of the area the user can actually see (shrinks when the keyboard opens). */
  height: number;
  /** How far the browser has panned the visual viewport down the layout viewport. */
  offsetTop: number;
  /** True while an on-screen keyboard covers the bottom of the layout viewport. */
  keyboardOpen: boolean;
}

/**
 * Read the metrics, recording the layout's resting height per layout width in
 * `restingHeights` — the baseline a `resizes-content` keyboard is measured
 * against (see ResizedKeyboardEvidence).
 */
function read(vv: VisualViewport, restingHeights: Map<number, number>): VisualViewportMetrics {
  const width = window.innerWidth;
  const restingLayoutHeight = Math.max(restingHeights.get(width) ?? 0, window.innerHeight);
  restingHeights.set(width, restingLayoutHeight);
  return measureVisualViewport(vv, window.innerHeight, {
    restingLayoutHeight,
    editableFocused: isEditableElement(document.activeElement),
  });
}

/**
 * The visual viewport's size and pan offset, or null where the browser has no
 * `visualViewport` (jsdom, old browsers) and `100dvh` is all there is.
 *
 * `dvh` follows the URL bar but NOT the on-screen keyboard on iOS Safari: it
 * leaves the layout viewport full height when the keyboard opens and only
 * shrinks the visual one, so a `100dvh` shell keeps a composer underneath the
 * keyboard. (Chromium and Firefox resize the layout viewport instead, under
 * index.html's `interactive-widget=resizes-content`; `keyboardOpen` still
 * reports the keyboard there.) Anything that has to fit "what is on screen" reads
 * this instead. Pass `enabled=false` to opt out without subscribing.
 */
export function useVisualViewport(enabled = true): VisualViewportMetrics | null {
  // Per hook instance: the baseline is learned from what this instance has
  // seen, so a test (or a remount) starts clean.
  const [restingHeights] = useState(() => new Map<number, number>());
  const [metrics, setMetrics] = useState<VisualViewportMetrics | null>(() =>
    enabled && typeof window !== "undefined" && window.visualViewport
      ? read(window.visualViewport, restingHeights)
      : null,
  );

  useEffect(() => {
    const vv = window.visualViewport;
    if (!enabled || !vv) return;
    const update = () =>
      setMetrics((prev) => {
        const next = read(vv, restingHeights);
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
    // Focus is half of the resizes-content evidence. Chromium usually focuses
    // the field before the resize lands, but re-read on focus changes so the
    // order never matters. Deferred so moving focus field-to-field reads the
    // settled element instead of the empty moment between focusout and focusin.
    let focusTimer: ReturnType<typeof setTimeout> | undefined;
    const onFocusChange = () => {
      clearTimeout(focusTimer);
      focusTimer = setTimeout(update, 0);
    };
    document.addEventListener("focusin", onFocusChange);
    document.addEventListener("focusout", onFocusChange);
    return () => {
      vv.removeEventListener("resize", update);
      vv.removeEventListener("scroll", update);
      window.removeEventListener("resize", update);
      document.removeEventListener("focusin", onFocusChange);
      document.removeEventListener("focusout", onFocusChange);
      clearTimeout(focusTimer);
    };
  }, [enabled, restingHeights]);

  return enabled ? metrics : null;
}
