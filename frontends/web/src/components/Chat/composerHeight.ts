/** Desktop cap: lines of text before the composer scrolls. */
export const MAX_VISIBLE_LINES = 10;

/** Hard ceiling in px, desktop and mobile alike. */
export const COMPOSER_MAX_PX = 200;

/** Mobile: the most of the visible viewport the composer's text may take. */
export const MOBILE_COMPOSER_VIEWPORT_SHARE = 0.2;

/** Mobile: never squeeze the text area below this, however short the viewport. */
export const MOBILE_COMPOSER_MIN_PX = 72;

interface ComposerHeightInput {
  lineHeight: number;
  paddingY: number;
  /** Visible viewport height in px; set only where the cap should follow it (mobile). */
  viewportHeight?: number;
}

/**
 * The height the composer stops growing at, after which it scrolls inside.
 *
 * On a phone with the keyboard open the visible viewport is ~400-500px and the
 * header, composer toolbar and keyboard take most of it, so ten lines (200px)
 * left the timeline with a sliver. The mobile cap is a share of what is on
 * screen, clamped to a floor (a few lines stay usable) and the desktop ceiling.
 */
export function composerMaxHeight({ lineHeight, paddingY, viewportHeight }: ComposerHeightInput): number {
  const oneLine = lineHeight + paddingY;
  if (viewportHeight === undefined) {
    return Math.min(lineHeight * MAX_VISIBLE_LINES + paddingY, COMPOSER_MAX_PX);
  }
  const share = Math.floor(viewportHeight * MOBILE_COMPOSER_VIEWPORT_SHARE);
  const capped = Math.min(Math.max(share, MOBILE_COMPOSER_MIN_PX), COMPOSER_MAX_PX);
  return Math.max(capped, oneLine);
}
