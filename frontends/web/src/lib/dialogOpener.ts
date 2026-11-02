/**
 * The element a dialog hands focus back to when it closes.
 *
 * Restoring "whatever had focus when the dialog opened" is not enough on its
 * own: Safari (macOS and iOS) never focuses a button on click or tap, so a
 * dialog opened by tapping one finds <body> — or the field the user was typing
 * in — focused, and closing it strands keyboard and VoiceOver users away from
 * the control they used. So the control the user activated in the task that
 * opened the dialog wins; document.activeElement is the fallback for a dialog
 * opened without one (a shortcut, a programmatic open).
 *
 * A dialog must call `dialogOpener()` while it opens — in the effect that runs
 * for the activating click — not later: the record lasts one task.
 */

const ACTIVATABLE = 'a[href], button, input, select, textarea, summary, [tabindex]:not([tabindex="-1"])';

let activated: HTMLElement | null = null;
let clearScheduled = false;

if (typeof document !== "undefined") {
  // Capture phase: recorded before any handler that opens a dialog runs.
  document.addEventListener(
    "click",
    (event) => {
      const target = event.target instanceof Element ? event.target.closest<HTMLElement>(ACTIVATABLE) : null;
      activated = target;
      if (clearScheduled) return;
      clearScheduled = true;
      setTimeout(() => {
        activated = null;
        clearScheduled = false;
      }, 0);
    },
    true,
  );
}

export function dialogOpener(): HTMLElement | null {
  if (activated?.isConnected) return activated;
  // <body> is not an opener: focus falls back to it when the focused element
  // unmounts in the same render that opens the dialog (a menu item that
  // closes its menu), so treat it as "the opener is gone".
  const active = document.activeElement;
  return active instanceof HTMLElement && active !== document.body ? active : null;
}
