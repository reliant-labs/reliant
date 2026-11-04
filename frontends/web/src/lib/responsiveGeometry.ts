/** Geometry only: never use visual (zoomed/keyboard) width to choose a route. */
export interface VisualViewportGeometry {
  height: number;
  offsetTop: number;
  scale: number;
}

/**
 * Evidence for a keyboard that RESIZED the layout viewport rather than
 * overlaying it.
 *
 * With `interactive-widget=resizes-content` (index.html) Chromium and Firefox
 * shrink `innerHeight` together with the visual viewport, so "layout minus
 * visual" is ~0 with the keyboard up. What still shows it is the drop from the
 * height this layout had at rest — gated on a focused text field, because a
 * keyboard only opens for one and an unrelated height drop (a desktop window
 * resized on `/m/*`, a split-screen) must not read as one.
 */
export interface ResizedKeyboardEvidence {
  /**
   * The tallest layout height seen at the CURRENT layout width. Keyed by width
   * so a rotation never compares against the other orientation's height.
   */
  restingLayoutHeight: number;
  /** An editable element has focus, so an on-screen keyboard may be up. */
  editableFocused: boolean;
}

/** Browser chrome can consume ~110px; a docked keyboard is well past it. */
const KEYBOARD_MIN_OCCLUSION_PX = 150;

/**
 * This is an occlusion heuristic, not a keyboard API: floating keyboards may
 * not be detectable. Two shapes are recognised:
 *
 * - overlay (WebKit, and Chromium without the meta): the layout viewport keeps
 *   its height and the visual one shrinks under it;
 * - resize (`resizes-content`): both shrink, measured against the resting
 *   height in `resized` — see ResizedKeyboardEvidence.
 */
export function measureVisualViewport(
  viewport: VisualViewportGeometry,
  layoutHeight: number,
  resized?: ResizedKeyboardEvidence,
) {
  const overlaid = layoutHeight - viewport.height > KEYBOARD_MIN_OCCLUSION_PX;
  const resizedForKeyboard =
    resized !== undefined &&
    resized.editableFocused &&
    resized.restingLayoutHeight - viewport.height > KEYBOARD_MIN_OCCLUSION_PX;
  return {
    height: Math.round(viewport.height),
    offsetTop: Math.round(viewport.offsetTop),
    // Zoom in OR out is not keyboard occlusion. Leave zoom/panning to the UA.
    keyboardOpen: Math.abs(viewport.scale - 1) <= 0.01 && (overlaid || resizedForKeyboard),
  };
}

const NON_TEXT_INPUT_TYPES = new Set([
  "button", "checkbox", "color", "file", "hidden", "image", "radio", "range", "reset", "submit",
]);

/** Whether focusing this element raises an on-screen keyboard. */
export function isEditableElement(el: Element | null): boolean {
  if (!el) return false;
  if (el instanceof HTMLTextAreaElement) return !el.readOnly && !el.disabled;
  if (el instanceof HTMLInputElement) {
    return !NON_TEXT_INPUT_TYPES.has(el.type) && !el.readOnly && !el.disabled;
  }
  // `=== true`: jsdom leaves isContentEditable undefined.
  return el instanceof HTMLElement && el.isContentEditable === true;
}
