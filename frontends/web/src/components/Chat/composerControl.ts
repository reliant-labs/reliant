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
