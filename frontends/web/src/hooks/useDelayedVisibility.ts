import { useEffect, useRef, useState } from "react";

/**
 * Visibility for a loading indicator that must neither flash nor flicker.
 *
 * Becomes true only once `active` has held for `showDelayMs` (so work that
 * finishes quickly shows nothing), and once true stays true for at least
 * `minVisibleMs` (so a state that clears just after appearing does not blink).
 * `active` returning during that minimum keeps it up without a gap.
 */
export function useDelayedVisibility(
  active: boolean,
  { showDelayMs, minVisibleMs }: { showDelayMs: number; minVisibleMs: number },
): boolean {
  const [visible, setVisible] = useState(false);
  const shownAtRef = useRef(0);

  useEffect(() => {
    if (active === visible) return;
    const delay = active
      ? showDelayMs
      : Math.max(0, minVisibleMs - (Date.now() - shownAtRef.current));
    const timer = setTimeout(() => {
      if (active) shownAtRef.current = Date.now();
      setVisible(active);
    }, delay);
    return () => clearTimeout(timer);
  }, [active, visible, showDelayMs, minVisibleMs]);

  return visible;
}
