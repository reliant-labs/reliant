import { useSyncExternalStore } from "react";

/**
 * Whether the PRIMARY pointer is coarse (a finger), the same signal the
 * `touch:` variant and the mobile redirect read.
 *
 * This is an input-method fact, not a layout one: the composer keys its
 * density off its container width, and uses this only for behavior that
 * depends on how the user types and taps — Return inserting a newline on a
 * touch keyboard, and the model pill staying off the composer on touch
 * layouts (dev-docs/design/mobile-chat-redesign.md §5.3, D1, D2).
 *
 * Subscribed, not read once: an iPad gaining a trackpad, or a convertible
 * folding its keyboard away, changes the answer without a reload.
 */
const QUERY = "(pointer: coarse)";

function mediaQuery(): MediaQueryList | null {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return null;
  return window.matchMedia(QUERY);
}

function subscribe(onChange: () => void): () => void {
  const mql = mediaQuery();
  if (!mql) return () => {};
  mql.addEventListener("change", onChange);
  return () => mql.removeEventListener("change", onChange);
}

function snapshot(): boolean {
  return mediaQuery()?.matches ?? false;
}

export function useCoarsePointer(): boolean {
  return useSyncExternalStore(subscribe, snapshot, () => false);
}
