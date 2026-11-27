// Copyright (c) 2026 Reliant Labs

/**
 * Whether Settings › Developer is offered: dev builds, and a server that says
 * it is one (`RELIANT_CONFIG.isDev`). Settings' own nav
 * (Settings/SettingsNavigation.tsx) and the command palette
 * (Layout/CommandPalette.tsx) apply the same rule. It lives here, not in
 * Settings, so the palette can be mounted where the Settings page is not (the
 * public demo).
 */
export function showDeveloperSettings(): boolean {
  return import.meta.env.DEV || Boolean(window.RELIANT_CONFIG?.isDev);
}
