/**
 * The size and focus ring every composer control shares (workflow, presets,
 * toolbar params, model, settings, attach, send/stop).
 *
 * In px, not rem, on purpose: the root font size follows the user's text-size
 * setting (12–16px), and a pointer target must not shrink with it. 32px clears
 * WCAG 2.2's 24px minimum with room for an icon and a label; touch screens
 * still get their own 44px (`touch:` variants at each use).
 */
export const COMPOSER_CONTROL =
  "h-[32px] min-w-[32px] focus:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background";

/**
 * The composer's round icon buttons at the ends of its row: ⊕ on the left,
 * the send/stop/queue slot on the right. 32px with a fine pointer, 44px on
 * touch (design doc §5.3), always a circle.
 */
export const COMPOSER_ROUND_BUTTON = `${COMPOSER_CONTROL} relative inline-flex w-[32px] shrink-0 items-center justify-center rounded-full transition-colors touch:h-[44px] touch:w-[44px]`;
