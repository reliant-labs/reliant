import { useEffect, useRef, useState } from "react";

/**
 * `value`, but each shown value stays up for at least `minMs` before a newer
 * one replaces it. A fast agent finishes a read in tens of milliseconds, so a
 * status line that followed every step would flash text nobody can read;
 * this holds each step long enough to register. Only the newest pending value
 * is ever shown — intermediate ones that arrive during the hold are skipped,
 * so the line never lags further behind than `minMs`.
 */
export function useMinimumDwell<T>(value: T, minMs: number): T {
  const [shown, setShown] = useState(value);
  const shownAt = useRef(Date.now());

  useEffect(() => {
    if (Object.is(value, shown)) return;
    const show = () => {
      shownAt.current = Date.now();
      setShown(value);
    };
    const wait = shownAt.current + minMs - Date.now();
    if (wait <= 0) {
      show();
      return;
    }
    const timer = setTimeout(show, wait);
    return () => clearTimeout(timer);
  }, [value, shown, minMs]);

  return shown;
}
