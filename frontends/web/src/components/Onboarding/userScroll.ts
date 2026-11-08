/**
 * Whether the reader has started scrolling the page themselves.
 *
 * A tour scrolls its target into view when it arrives. Once the reader has
 * taken over, it must not: a target that turns up late (a lazy chunk, data
 * still loading) would otherwise yank the page back while they read, and a
 * spotlight that tracks a moving target trails a finger-driven scroll on a
 * phone, whose scrolling runs off the main thread.
 *
 * Read from input, never from `scroll` events: the tour's own scrollIntoView
 * and content growing in both fire those, and neither is the reader.
 */

/** Keys that scroll the focused scroller. */
const SCROLL_KEYS = new Set(["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "]);

/** Call `onScroll` (once) when the reader first drags, wheels or key-scrolls. Returns the unsubscribe. */
export function watchUserScroll(onScroll: () => void): () => void {
  let done = false;
  const stop = () => {
    window.removeEventListener("touchmove", fire, true);
    window.removeEventListener("wheel", fire, true);
    window.removeEventListener("keydown", onKey, true);
  };
  function fire() {
    if (done) return;
    done = true;
    stop();
    onScroll();
  }
  function onKey(event: KeyboardEvent) {
    if (SCROLL_KEYS.has(event.key)) fire();
  }
  window.addEventListener("touchmove", fire, { capture: true, passive: true });
  window.addEventListener("wheel", fire, { capture: true, passive: true });
  window.addEventListener("keydown", onKey, true);
  return () => {
    done = true;
    stop();
  };
}

/**
 * Hold the page still: cancel the reader's drags, wheels and scrolling keys,
 * except inside `allowed()` (a sheet that scrolls its own content). Keys are
 * held only when nothing is focused, so typing into the app is untouched.
 * Returns the release.
 */
export function holdUserScroll(allowed: () => Element | null): () => void {
  const inside = (event: Event) => {
    const box = allowed();
    return box !== null && event.target instanceof Node && box.contains(event.target);
  };
  const onMove = (event: Event) => {
    if (!inside(event)) event.preventDefault();
  };
  const onKey = (event: KeyboardEvent) => {
    const unfocused = event.target === document.body || event.target === document.documentElement;
    if (unfocused && SCROLL_KEYS.has(event.key)) event.preventDefault();
  };
  window.addEventListener("touchmove", onMove, { capture: true, passive: false });
  window.addEventListener("wheel", onMove, { capture: true, passive: false });
  window.addEventListener("keydown", onKey, true);
  return () => {
    window.removeEventListener("touchmove", onMove, true);
    window.removeEventListener("wheel", onMove, true);
    window.removeEventListener("keydown", onKey, true);
  };
}
