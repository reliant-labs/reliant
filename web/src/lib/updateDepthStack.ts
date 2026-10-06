/**
 * Make React's "Maximum update depth exceeded" warning say WHO is looping.
 *
 * React 19 logs the passive-effect variant of this warning as a bare
 * `console.error(message)` — no component stack, no error object — so the dev
 * log recorded it 27 times during one session with nothing to attribute it to
 * (gtm/reviews/product-ux-findings.md A-5). But React emits it from inside the
 * state update itself (dispatchSetState → scheduleUpdateOnFiber →
 * getRootForUpdatedFiber), so the JavaScript stack at that moment runs through
 * the exact app frame that called the setter. Appending that stack makes the
 * next occurrence diagnosable from the log alone.
 *
 * Dev-only, and it changes nothing about the warning except to add the stack.
 */

const UPDATE_DEPTH_PREFIX = "Maximum update depth exceeded";

let installed = false;

export function installUpdateDepthStack(): void {
  if (installed || typeof console === "undefined") return;
  installed = true;
  const previous = console.error;
  console.error = (...args: unknown[]) => {
    if (typeof args[0] === "string" && args[0].startsWith(UPDATE_DEPTH_PREFIX)) {
      const stack = new Error("state update that exceeded the depth limit").stack ?? "";
      previous.apply(console, [...args, `\n${stack}`]);
      return;
    }
    previous.apply(console, args);
  };
}
