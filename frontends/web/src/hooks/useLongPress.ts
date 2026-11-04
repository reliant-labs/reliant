import { useCallback, useEffect, useRef } from "react";

const LONG_PRESS_MS = 500;
// Movement past this many px cancels the press — without it, the start of a
// virtualized-list scroll (which begins as a touchstart indistinguishable
// from a long-press) would fire the action underneath the user's thumb.
const MOVE_CANCEL_PX = 10;

export interface LongPressHandlers {
  onTouchStart: (e: React.TouchEvent) => void;
  onTouchMove: (e: React.TouchEvent) => void;
  onTouchEnd: (e: React.TouchEvent) => void;
  onTouchCancel: (e: React.TouchEvent) => void;
  onContextMenu: (e: React.SyntheticEvent) => void;
}

/**
 * Classes for any element that carries the long-press handlers. iOS starts a
 * native text selection (plus the callout) at about the same 500ms the press
 * fires; a selection anchored in the message extends to the end of the
 * document and ends up under the actions sheet, where a tap toggles the
 * system edit menu instead of dispatching `click`. "Copy message" in the
 * sheet replaces native copy on these surfaces.
 */
export const LONG_PRESS_SURFACE_CLASS = "select-none [-webkit-touch-callout:none]";

/**
 * Long-press detection for touch surfaces. Fires `onLongPress` once the
 * finger has been down for `LONG_PRESS_MS` without moving more than
 * `MOVE_CANCEL_PX`. A short tap (release before the timer) and a scroll
 * (move past the threshold) both cancel silently — neither invokes the
 * callback.
 */
export function useLongPress(onLongPress: () => void): LongPressHandlers {
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const startRef = useRef<{ x: number; y: number } | null>(null);
  const firedRef = useRef(false);
  const removeListenersRef = useRef<(() => void) | null>(null);

  const clear = useCallback(() => {
    removeListenersRef.current?.();
    removeListenersRef.current = null;
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    startRef.current = null;
  }, []);

  useEffect(() => clear, [clear]);

  const onTouchStart = useCallback(
    (e: React.TouchEvent) => {
      clear();
      firedRef.current = false;
      const touch = e.touches[0];
      if (!touch || e.touches.length !== 1) return;
      // Global cancellation exists only for the active gesture, never per
      // mounted transcript row. Capture catches nested scrollers too.
      document.addEventListener("scroll", clear, true);
      window.addEventListener("blur", clear);
      document.addEventListener("visibilitychange", clear);
      removeListenersRef.current = () => {
        document.removeEventListener("scroll", clear, true);
        window.removeEventListener("blur", clear);
        document.removeEventListener("visibilitychange", clear);
      };
      startRef.current = { x: touch.clientX, y: touch.clientY };
      firedRef.current = false;
      timerRef.current = setTimeout(() => {
        clear();
        firedRef.current = true;
        // Drop any selection the browser already began for this press, so it
        // can't survive under the sheet that is about to open.
        window.getSelection()?.removeAllRanges();
        onLongPress();
      }, LONG_PRESS_MS);
    },
    [onLongPress, clear],
  );

  const onTouchMove = useCallback(
    (e: React.TouchEvent) => {
      const start = startRef.current;
      const touch = e.touches[0];
      if (!start || !touch) return;
      const dx = Math.abs(touch.clientX - start.x);
      const dy = Math.abs(touch.clientY - start.y);
      if (dx > MOVE_CANCEL_PX || dy > MOVE_CANCEL_PX) {
        clear();
      }
    },
    [clear],
  );

  const onTouchEnd = useCallback(
    (e: React.TouchEvent) => {
      // A long-press that already fired opens the sheet; the subsequent
      // touchend is not a tap and must not fall through to a click handler.
      if (firedRef.current) {
        e.preventDefault();
      }
      clear();
    },
    [clear],
  );

  const onTouchCancel = useCallback(() => {
    clear();
  }, [clear]);

  // Android Chrome opens its own selection/context menu on long-press.
  const onContextMenu = useCallback((e: React.SyntheticEvent) => {
    e.preventDefault();
  }, []);

  return { onTouchStart, onTouchMove, onTouchEnd, onTouchCancel, onContextMenu };
}
