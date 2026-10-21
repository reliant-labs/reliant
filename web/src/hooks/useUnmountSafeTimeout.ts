import { useCallback, useEffect, useRef } from "react";

/**
 * A setTimeout whose timers die with the component.
 *
 * A bare `setTimeout(() => setState(...), ms)` in an event handler or a
 * promise continuation outlives the component that scheduled it. In the app
 * that is a state update on an unmounted tree; under vitest it is worse — the
 * timer fires after the test file's jsdom environment has been torn down, React
 * reads `window` to pick an update lane, and the run fails with an uncaught
 * `ReferenceError: window is not defined` attributed to whichever file happened
 * to be running.
 *
 * The returned `schedule(fn, ms)` is stable. Every pending timer is cleared on
 * unmount, a timer that has already been queued never runs `fn` after unmount,
 * and a call made after unmount (an awaited promise resolving late) schedules
 * nothing.
 */
export function useUnmountSafeTimeout(): (fn: () => void, ms: number) => void {
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>());
  // True from the first render, so a sibling effect that runs before this
  // hook's own effect can still schedule; only unmount turns it off (and a
  // StrictMode remount turns it back on).
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    const pending = timers.current;
    return () => {
      mounted.current = false;
      pending.forEach(clearTimeout);
      pending.clear();
    };
  }, []);

  return useCallback((fn: () => void, ms: number) => {
    if (!mounted.current) return;
    const id = setTimeout(() => {
      timers.current.delete(id);
      if (mounted.current) fn();
    }, ms);
    timers.current.add(id);
  }, []);
}
