/**
 * Which form the pinned prompt ("jump to your last message") takes.
 *
 * Keyed on SPACE, not surface (`src/lib/surface.ts`): a phone-width pane gets
 * the one-line strip whether it is `/m/*` or a narrow desktop split, and a
 * tablet at medium width keeps the desktop overlay. See
 * `dev-docs/design/mobile-chat-redesign.md` §7.2.
 *
 * - `overlay`: today's floating bubble with a Jump-to button (medium and up).
 * - `strip`:   a single 36px line, the whole of it the jump target (compact).
 * - `hidden`:  none at all — the keyboard is up (compose mode) or the viewport
 *              is a compact-height landscape phone. You are writing the next
 *              turn or have ~350px of height; the previous prompt is the least
 *              useful thing on screen.
 */

import { useEffect, useState } from "react";
import { useComposeMode } from "../../../lib/composeMode";

export type PinnedPromptForm = "overlay" | "strip" | "hidden";

/** Material 3's compact width class: the transcript pane, not the window. */
export const PINNED_STRIP_MAX_WIDTH_PX = 600;
/** Material 3's compact height class: a phone in landscape. */
export const PINNED_PROMPT_MIN_VIEWPORT_HEIGHT_PX = 480;

export function pinnedPromptForm({
  paneWidth,
  viewportHeight,
  composeMode,
}: {
  /** The transcript pane's width; 0 when it has not been laid out yet. */
  paneWidth: number;
  viewportHeight: number;
  composeMode: boolean;
}): PinnedPromptForm {
  if (composeMode) return "hidden";
  if (viewportHeight > 0 && viewportHeight < PINNED_PROMPT_MIN_VIEWPORT_HEIGHT_PX) return "hidden";
  // An unmeasured pane (0) is not evidence of a narrow one: keep the overlay
  // until the first measurement, which lands before the first paint.
  if (paneWidth > 0 && paneWidth < PINNED_STRIP_MAX_WIDTH_PX) return "strip";
  return "overlay";
}

/**
 * Track the pane width and viewport height that pick the pinned prompt's form.
 * Takes the element rather than a ref so the subscription follows the pane
 * when it mounts after the first render (an empty transcript renders none).
 */
export function usePinnedPromptForm(pane: HTMLElement | null): PinnedPromptForm {
  const composeMode = useComposeMode();
  const [paneWidth, setPaneWidth] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(() =>
    typeof window === "undefined" ? 0 : window.innerHeight,
  );

  useEffect(() => {
    // Layout height, not the visual viewport: the keyboard is compose mode's
    // business, and counting it here would make "compact height" true every
    // time a phone in portrait opens its keyboard.
    const read = () => {
      setViewportHeight(window.innerHeight);
      if (pane) setPaneWidth(pane.clientWidth);
    };
    read();
    window.addEventListener("resize", read);
    let observer: ResizeObserver | undefined;
    if (pane && typeof ResizeObserver !== "undefined") {
      observer = new ResizeObserver(read);
      observer.observe(pane);
    }
    return () => {
      window.removeEventListener("resize", read);
      observer?.disconnect();
    };
  }, [pane]);

  return pinnedPromptForm({ paneWidth, viewportHeight, composeMode });
}

/** The strip's text: the prompt's first non-blank line. */
export function pinnedPromptFirstLine(text: string): string {
  return text.split("\n").find((line) => line.trim() !== "")?.trim() ?? "";
}
