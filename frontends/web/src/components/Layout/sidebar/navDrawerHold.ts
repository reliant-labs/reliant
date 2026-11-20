// Copyright (c) 2026 Reliant Labs

/**
 * Keeping a nav drawer open across a drill-in (U6e, mobile). A drawer — the
 * narrow window's (Layout/shell/AppShell) or the phone's
 * (Mobile/MobileNavDrawer) — closes when the page changes, so a tapped
 * destination is what you see next. Drilling into Agents or Observability (or
 * back out) changes the page too, but the point of that tap is the sub-nav: the
 * nav holds the drawer open for the navigation it starts (NavRenderer's
 * `drill`), and the drawer's close-on-navigate asks here first.
 */

let holdUntil = 0;

/** Keep any open nav drawer open through the navigation about to start (a redirect included). */
export function holdNavDrawerOpen(ms = 2000): void {
  holdUntil = Date.now() + ms;
}

/** Whether a navigation now was started by a drill-in, so the drawer stays open. */
export function navDrawerHeld(): boolean {
  return Date.now() < holdUntil;
}
