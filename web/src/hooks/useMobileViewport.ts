import { useEffect, useState } from "react";

interface MobileViewport {
  width: number;
  height: number;
  top: number;
  left: number;
  keyboardVisible: boolean;
}

function readViewport(): MobileViewport {
  const viewport = window.visualViewport;
  return {
    width: viewport?.width ?? window.innerWidth,
    height: viewport?.height ?? window.innerHeight,
    top: viewport?.offsetTop ?? 0,
    left: viewport?.offsetLeft ?? 0,
    keyboardVisible: false,
  };
}

/** Follow the visible browser area, including Safari's keyboard-induced pan. */
export function useMobileViewport(enabled = true): MobileViewport {
  const [bounds, setBounds] = useState(readViewport);
  useEffect(() => {
    if (!enabled) return;
    const viewport = window.visualViewport;
    let frame = 0;
    let unfocusedHeight = viewport?.height ?? window.innerHeight;
    let previousWidth = viewport?.width ?? window.innerWidth;
    const update = () => {
      frame = 0;
      // Preserve native pinch zoom; it must not resize/reflow the app.
      if (viewport && viewport.scale !== 1) return;
      const next = readViewport();
      const active = document.activeElement;
      const editing = active instanceof HTMLElement && (
        active.matches("textarea, input:not([type=checkbox]):not([type=radio]):not([type=button]):not([type=submit])") || active.isContentEditable
      );
      if (!editing || next.width !== previousWidth) {
        unfocusedHeight = Math.max(next.height, window.innerHeight, document.documentElement.clientHeight);
      }
      previousWidth = next.width;
      const layoutHeight = Math.max(unfocusedHeight, window.innerHeight, document.documentElement.clientHeight);
      next.keyboardVisible = editing && layoutHeight - next.height > 100;
      setBounds(next);
    };
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(update);
    };
    update();
    viewport?.addEventListener("resize", schedule);
    viewport?.addEventListener("scroll", schedule);
    window.addEventListener("resize", schedule);
    document.addEventListener("focusin", schedule);
    document.addEventListener("focusout", schedule);
    return () => {
      cancelAnimationFrame(frame);
      viewport?.removeEventListener("resize", schedule);
      viewport?.removeEventListener("scroll", schedule);
      window.removeEventListener("resize", schedule);
      document.removeEventListener("focusin", schedule);
      document.removeEventListener("focusout", schedule);
    };
  }, [enabled]);
  return bounds;
}
