/**
 * Poll interval that stops while the tab is hidden.
 *
 * On a phone this is not an optimization, it is a correctness constraint:
 * a backgrounded PWA that keeps a 5s poll running burns battery and control-
 * plane quota for a screen nobody is looking at, and iOS will eventually
 * throttle or kill the tab in ways that make the resumed state confusing.
 *
 * React Query's `refetchIntervalInBackground: false` only covers *window
 * focus*, which is not the same event: switching apps on iOS fires
 * `visibilitychange` but not always `blur`, and a phone locked mid-session
 * leaves a focused-but-hidden document polling forever. This hook tracks the
 * real signal and returns `false` — TanStack's "don't poll" value — while
 * hidden, so the query stops rather than merely deferring.
 *
 * Returning a value (instead of subscribing inside the query) keeps the
 * decision testable and lets a caller compose it with its own conditions.
 */

import { useEffect, useState } from "react";

/** Whether the document is currently visible. */
export function useDocumentVisible(): boolean {
  const [visible, setVisible] = useState(
    // jsdom and SSR have no visibilityState; assume visible so tests and the
    // first paint behave like a foregrounded tab.
    () =>
      typeof document === "undefined" || document.visibilityState !== "hidden",
  );

  useEffect(() => {
    if (typeof document === "undefined") return;
    const onChange = () =>
      setVisible(document.visibilityState !== "hidden");
    // Re-read on mount: the tab can already be hidden by the time this runs
    // (restored background tab), and no event will fire to tell us.
    onChange();
    document.addEventListener("visibilitychange", onChange);
    return () => document.removeEventListener("visibilitychange", onChange);
  }, []);

  return visible;
}

/**
 * `intervalMs` while the tab is visible, `false` while it is hidden — feed
 * straight into a React Query `refetchInterval`.
 */
export function useVisibilityPolling(intervalMs: number): number | false {
  return useDocumentVisible() ? intervalMs : false;
}
