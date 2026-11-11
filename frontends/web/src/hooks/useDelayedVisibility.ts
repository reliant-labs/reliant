import { useEffect, useState } from "react";

/**
 * A loading indicator's state:
 *
 *   hidden   nothing on screen
 *   shown    up, and standing for the work: what it covers is not usable yet
 *   leaving  the work is done and what it covers is usable now; the indicator
 *            is only still painted so it can fade out rather than blink out
 */
export type DelayedVisibility = "hidden" | "shown" | "leaving";

/**
 * Visibility for a loading indicator that must neither flash nor hold up the
 * content it covers.
 *
 * "shown" only once `active` has held for `showDelayMs`, so work that finishes
 * quickly shows nothing. The render in which `active` ends is already
 * "leaving" — never a minimum time "shown", which would keep finished content
 * covered for nothing — and "leaving" lasts `exitMs`, long enough to fade.
 * `active` returning while it leaves brings it straight back to "shown",
 * without the delay and without a gap.
 */
export function useDelayedVisibility(
  active: boolean,
  { showDelayMs, exitMs }: { showDelayMs: number; exitMs: number },
): DelayedVisibility {
  // Painted: shown, or still leaving.
  const [up, setUp] = useState(false);

  useEffect(() => {
    if (active === up) return;
    const timer = setTimeout(() => setUp(active), active ? showDelayMs : exitMs);
    return () => clearTimeout(timer);
  }, [active, up, showDelayMs, exitMs]);

  if (!up) return "hidden";
  return active ? "shown" : "leaving";
}
