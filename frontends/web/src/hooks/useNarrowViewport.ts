/**
 * Whether the viewport is narrower than Tailwind's `md` (768px): a phone.
 *
 * For layout that CSS alone cannot switch — a virtualizer that must measure
 * stacked rows instead of assuming a fixed height, a waterfall that drops a
 * column. Anything a breakpoint class can express should use one instead.
 *
 * This is width, deliberately not pointer type: it answers "does the desktop
 * layout fit?", which is a width question. Touch sizing is the other axis
 * (index.css, `touch:` / `touch-target`).
 */

import { useSyncExternalStore } from "react";

export const NARROW_VIEWPORT_QUERY = "(max-width: 767px)";

function subscribe(onChange: () => void): () => void {
  const query = window.matchMedia?.(NARROW_VIEWPORT_QUERY);
  if (!query) return () => {};
  query.addEventListener("change", onChange);
  return () => query.removeEventListener("change", onChange);
}

function snapshot(): boolean {
  return typeof window !== "undefined" && !!window.matchMedia?.(NARROW_VIEWPORT_QUERY).matches;
}

export function useNarrowViewport(): boolean {
  return useSyncExternalStore(subscribe, snapshot, () => false);
}
